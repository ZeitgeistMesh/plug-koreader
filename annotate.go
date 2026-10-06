package main

// annotate.go — starring and tagging your highlights, which this plug does not store.
//
// Stars and tags belong to vision (design §1.2): a star makes a core memory, vision's own
// editable copy of the highlight, and the highlight here is never written by anything but
// the device's sync. So koreader's controls CALL vision, and koreader keeps only a CACHE
// of the answer — per person, Ref → {starred, tags} — to draw its pages from.
//
// The rule from design §1.3: a write waits for its owner's acknowledgement; a page never
// does. So:
//
//   - a page reads the cache. When there is none yet (first visit after a restart), it
//     starts a fetch BEHIND the page and the controls say so for that one load, rather than
//     drawing a star state that may be wrong;
//   - a star or tag POST calls vision and, once vision acknowledges, writes the answer
//     through to the cache;
//   - vision publishes xats.vision.quote.changed when a star or tag changes on its side,
//     and the cache drops that person's entry.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"xf/controller/pkg/plugcfg"
	"xf/controller/pkg/quotebrowser"
)

const (
	capVisionAnnotations = "vision.quote.annotations"
	capVisionStar        = "vision.quote.star"
	capVisionTagSet      = "vision.tag.set"

	subjectVisionChanged = "xats.vision.quote.changed"

	// annTTL bounds how long a cached entry is trusted without an event, so a lost
	// event heals on its own.
	annTTL = 10 * time.Minute
)

// annotation is one highlight's state in vision.
type annotation struct {
	Starred bool     `json:"starred"`
	Tags    []string `json:"tags,omitempty"`
}

// visionPeer is how this plug calls vision as a person. Tests supply a fake.
type visionPeer interface {
	Call(ctx context.Context, jid, capability, caller string, args map[string]any) (map[string]any, error)
}

type rtVisionPeer struct{ a *app }

func (p rtVisionPeer) Call(ctx context.Context, jid, capability, caller string, args map[string]any) (map[string]any, error) {
	if p.a.rt == nil {
		return nil, fmt.Errorf("not connected")
	}
	ev, err := p.a.rt.CallPeer(ctx, jid, capability, caller, args)
	if err != nil {
		return nil, err
	}
	if e, _ := ev.Result.Meta["error"].(string); strings.TrimSpace(e) != "" {
		return nil, fmt.Errorf("%s", e)
	}
	return ev.Result.Meta, nil
}

type annEntry struct {
	at      time.Time
	items   map[string]annotation
	err     string
	loading bool
}

type annCache struct {
	mu sync.Mutex
	m  map[string]*annEntry
}

func (a *app) visionJID() string { return plugcfg.Env("PLUG_KOREADER_VISION_JID", plugcfg.Svc("vision")) }

func (a *app) annCacheFor() *annCache {
	a.annOnce.Do(func() {
		if a.ann == nil {
			a.ann = &annCache{m: map[string]*annEntry{}}
		}
	})
	return a.ann
}

// annotationsFor returns the cached state for a person, and whether there is any. It
// starts a refresh behind the caller when the entry is missing or old.
func (a *app) annotationsFor(caller string) (map[string]annotation, bool) {
	c := a.annCacheFor()
	c.mu.Lock()
	e := c.m[caller]
	stale := e == nil || (!e.loading && time.Since(e.at) > annTTL)
	if stale {
		if e == nil {
			e = &annEntry{}
			c.m[caller] = e
		}
		e.loading = true
	}
	items, have := e.items, e.items != nil
	c.mu.Unlock()
	if stale {
		go a.loadAnnotations(caller)
	}
	return items, have
}

func (a *app) loadAnnotations(caller string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	items := map[string]annotation{}
	var errText string
	if a.vision == nil {
		errText = "vision is not connected"
	} else if meta, err := a.vision.Call(ctx, a.visionJID(), capVisionAnnotations, caller, map[string]any{"source": sourceKoreader}); err != nil {
		errText = err.Error()
	} else if raw, err := json.Marshal(meta["items"]); err == nil {
		_ = json.Unmarshal(raw, &items)
	}
	c := a.annCacheFor()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[caller]
	if e == nil {
		e = &annEntry{}
		c.m[caller] = e
	}
	e.loading, e.at = false, time.Now()
	if errText != "" {
		// A failed fetch keeps the last good state: a known star is still true while
		// vision is briefly away.
		e.err = errText
		log.Printf("plug-koreader: vision annotations: %s", errText)
		return
	}
	e.items, e.err = items, ""
}

// dropAnnotations forgets a person's cached state, so the next page fetches it afresh.
func (a *app) dropAnnotations(caller string) {
	c := a.annCacheFor()
	c.mu.Lock()
	delete(c.m, caller)
	c.mu.Unlock()
}

// writeThrough records vision's acknowledged answer for one highlight.
func (a *app) writeThrough(caller, ref string, fn func(*annotation)) {
	c := a.annCacheFor()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[caller]
	if e == nil || e.items == nil {
		return // nothing cached to correct; the next page fetches the whole state
	}
	an := e.items[ref]
	fn(&an)
	e.items[ref] = an
}

// annotationsUnavailable is the browser's Config.Unavailable: a sentence instead of the
// controls when the star state is not known.
func (a *app) annotationsUnavailable(_ context.Context, caller string) string {
	if _, have := a.annotationsFor(caller); have {
		return ""
	}
	c := a.annCacheFor()
	c.mu.Lock()
	e := c.m[caller]
	failed := e != nil && e.err != ""
	c.mu.Unlock()
	if failed {
		return "Stars and tags are unavailable right now — they are kept in Vision."
	}
	return "Stars and tags are loading — reload in a moment."
}

// subscribeVisionChanged drops a person's cache when vision says their stars or tags moved.
func (a *app) subscribeVisionChanged() {
	if a.rt == nil || a.rt.Conn() == nil {
		return
	}
	_, err := a.rt.Conn().Subscribe(subjectVisionChanged, func(m *nats.Msg) {
		var ev struct {
			Subject string `json:"subject"`
		}
		if json.Unmarshal(m.Data, &ev) == nil && ev.Subject != "" {
			a.dropAnnotations(ev.Subject)
		}
	})
	if err != nil {
		log.Printf("plug-koreader: subscribe %s: %v", subjectVisionChanged, err)
	}
}

// koAnnotator is the browser's writes: every one is vision's, made as the person.
type koAnnotator struct{ a *app }

func (k koAnnotator) Star(ctx context.Context, caller string, q quotebrowser.Quote, on bool) error {
	if k.a.vision == nil {
		return fmt.Errorf("Vision is not connected")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// The copy travels with the star, so vision's memory has the words even before its
	// mirror has caught up with this highlight.
	cp := map[string]any{"text": q.Text, "note": q.Note, "book": q.Book, "author": q.Author, "chapter": q.Chapter,
		"book_key": q.BookKey, "page": q.Page}
	if !q.At.IsZero() {
		cp["annotated_at"] = q.At.UTC().Format(time.RFC3339)
	}
	if _, err := k.a.vision.Call(cctx, k.a.visionJID(), capVisionStar, caller,
		map[string]any{"ref": q.Ref.String(), "on": on, "copy": cp}); err != nil {
		return fmt.Errorf("Vision did not save the star: %v", err)
	}
	k.a.writeThrough(caller, q.Ref.String(), func(an *annotation) { an.Starred = on })
	return nil
}

func (k koAnnotator) Tag(ctx context.Context, caller string, ref quotebrowser.Ref, add, remove []string) error {
	if k.a.vision == nil {
		return fmt.Errorf("Vision is not connected")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	meta, err := k.a.vision.Call(cctx, k.a.visionJID(), capVisionTagSet, caller,
		map[string]any{"ref_id": ref.String(), "ref_kind": "source", "add": add, "remove": remove})
	if err != nil {
		return fmt.Errorf("Vision did not save the tags: %v", err)
	}
	var tags []string
	if raw, err := json.Marshal(meta["tags"]); err == nil {
		_ = json.Unmarshal(raw, &tags)
	}
	k.a.writeThrough(caller, ref.String(), func(an *annotation) { an.Tags = tags })
	return nil
}

// A highlight is never edited here, and a typed quote or a grade belongs to vision's own
// pages; the koreader browser draws none of these controls.
func (koAnnotator) Add(context.Context, string, string, string, string) error {
	return fmt.Errorf("type a quote in Vision")
}

func (koAnnotator) Edit(context.Context, string, quotebrowser.Ref, string, string) error {
	return fmt.Errorf("the highlight from the book is never changed")
}

func (koAnnotator) Grade(context.Context, string, quotebrowser.Ref, string) error {
	return fmt.Errorf("review is in Vision")
}

func (koAnnotator) Suspend(context.Context, string, quotebrowser.Ref) error {
	return fmt.Errorf("review is in Vision")
}

// serveTag is the "tag from anywhere" host route for quotes shown outside the browser — the
// day page's annotation blocks. It is the browser's own tag action, through the same
// annotator, so a tag saved here is the same tag the reader shows.
func (a *app) serveTag(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	ref, ok := quotebrowser.ParseRef(r.FormValue("ref"), sourceKoreader)
	if !ok || r.FormValue("action") != "tag" || ref.Source != sourceKoreader {
		http.Error(w, "nothing to tag", http.StatusBadRequest)
		return
	}
	add, remove := quotebrowser.TagDiff(quotebrowser.NormalizeTags(r.FormValue("prev")), quotebrowser.NormalizeTags(r.FormValue("tags")))
	if len(add)+len(remove) > 0 {
		if err := (koAnnotator{a}).Tag(r.Context(), caller, ref, add, remove); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
