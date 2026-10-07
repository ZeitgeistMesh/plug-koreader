package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
	"xf/controller/pkg/quotebrowser"
	"xf/controller/pkg/webui"
)

// quoteFixture stores two books' highlights, deliberately out of both page and time order,
// plus a bookmark and a reading row that must never enter the browser.
func quoteFixture(t *testing.T, a *app) {
	t.Helper()
	const base = int64(1757000000)
	tk := tenant("reader@zm.is")
	for _, ev := range []ReadEvent{
		{ID: "b-p9", Kind: KindHighlight, BookMD5: "bbb", Title: "Second", Page: 9, StartTime: base + 50, Text: "nine"},
		{ID: "a-p2", Kind: KindHighlight, BookMD5: "aaa", Title: "First", Page: 2, StartTime: base + 900, Text: "two"},
		{ID: "a-p1", Kind: KindHighlight, BookMD5: "aaa", Title: "First", Page: 1, StartTime: base + 100, Text: "one"},
		{ID: "b-p3", Kind: KindHighlight, BookMD5: "bbb", Title: "Second", Page: 3, StartTime: base + 10, Text: "three"},
		{ID: "mark", Kind: KindBookmark, BookMD5: "aaa", Title: "First", Page: 4, StartTime: base},
		{ID: "read", Kind: KindRead, BookMD5: "aaa", Title: "First", Page: 1, StartTime: base, Duration: 300},
	} {
		ev := ev
		if err := a.events.Put(context.Background(), tk, ev.ID, &ev); err != nil {
			t.Fatal(err)
		}
	}
}

// browse renders one of the shared browser's pages as mounted by this plug.
func browse(t *testing.T, a *app, target string) string {
	t.Helper()
	mux := http.NewServeMux()
	a.quoteView().Mount(mux, a.webGate, a.page)
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d", target, w.Code)
	}
	return w.Body.String()
}

// The corpus is the highlights and nothing else: bookmarks without a page and reading rows
// never enter the browser.
func TestCorpusIsTheHighlights(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	qs, err := koCorpus{a}.List(context.Background(), "reader@zm.is")
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 4 {
		t.Fatalf("corpus has %d quotes, want 4 (bookmarks and read rows excluded)", len(qs))
	}
	for _, q := range qs {
		if q.Ref.Source != "koreader" || q.Ref.ID == "mark" || q.Ref.ID == "read" {
			t.Errorf("unexpected quote %v", q.Ref)
		}
		if q.AtHref == "" || !strings.HasPrefix(q.AtHref, "/day?d=") {
			t.Errorf("%v does not link its reading day: %q", q.Ref, q.AtHref)
		}
	}
}

// TestReaderPagesABookInReadingOrder: paging forward should walk a book the way it was read,
// and the book scope must survive Previous / Next or the second page of a book's quotes
// silently becomes the whole library.
func TestReaderPagesABookInReadingOrder(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	body := browse(t, a, "/quote?book=aaa")
	if !strings.Contains(body, ">one<") || !strings.Contains(body, "1 of 2") {
		t.Errorf("the scoped reader does not open on page 1 of the book's 2 quotes")
	}
	if !strings.Contains(body, `href="/quote?id=koreader%3Aa-p2&amp;book=aaa"`) {
		t.Errorf("Next does not lead to page 2 within the book")
	}
	if !strings.Contains(body, `href="/books"`) {
		t.Errorf("a scoped reader has no way back to the books")
	}
	if strings.Contains(body, "hx-get") {
		t.Errorf("the reader pager should be a full navigation, got an htmx swap")
	}
}

// A link from before Refs (/quote?id=<event id>) still opens its quote.
func TestAnOldQuoteLinkStillResolves(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	body := browse(t, a, "/quote?id=b-p9")
	if !strings.Contains(body, ">nine<") {
		t.Errorf("/quote?id=b-p9 did not open that quote")
	}
}

// fakeVision records koreader's calls and answers as vision would.
type fakeVision struct {
	mu    sync.Mutex
	calls []string
	anns  map[string]any
	fail  error
	hold  chan struct{} // when set, the annotations answer waits until it is closed
}

func (f *fakeVision) Call(_ context.Context, _, capability, caller string, args map[string]any) (map[string]any, error) {
	if capability == capVisionAnnotations && f.hold != nil {
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capability)
	if f.fail != nil {
		return nil, f.fail
	}
	switch capability {
	case capVisionAnnotations:
		return map[string]any{"items": f.anns}, nil
	case capVisionStar:
		cp, _ := args["copy"].(map[string]any)
		if cp["text"] == "" || cp == nil {
			return nil, fmt.Errorf("the star carried no copy")
		}
		return map[string]any{"ref": "vq:new", "starred": args["on"]}, nil
	case capVisionTagSet:
		return map[string]any{"tags": []any{"grit"}}, nil
	}
	return nil, fmt.Errorf("unexpected %s", capability)
}

func (f *fakeVision) count(capability string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == capability {
			n++
		}
	}
	return n
}

// waitLoaded lets the background annotation fetch land.
func waitLoaded(t *testing.T, a *app, caller string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, ok := a.annotationsFor(caller); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the annotation fetch never landed")
}

// The page never waits on vision: with nothing cached it says the stars are loading and
// fetches behind the page; the next load draws the real state.
func TestStarsComeFromTheCacheNeverAWait(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	fv := &fakeVision{anns: map[string]any{"koreader:a-p1": map[string]any{"starred": true, "tags": []any{"grit"}}},
		hold: make(chan struct{})}
	a.vision = fv
	// vision has not answered yet: the page must not wait for it, and must not guess.
	first := browse(t, a, "/quote?id=a-p1")
	if !strings.Contains(first, "Stars and tags are loading") || strings.Contains(first, `name="action"`) {
		t.Error("the first load drew controls whose state it could not know")
	}
	close(fv.hold)
	waitLoaded(t, a, "reader@zm.is")
	second := browse(t, a, "/quote?id=a-p1")
	if !strings.Contains(second, "</i> Starred</button>") || !strings.Contains(second, `value="grit"`) {
		t.Error("the cached star and tag were not drawn")
	}
	if fv.count(capVisionAnnotations) != 1 {
		t.Errorf("vision was asked %d times for one person's annotations", fv.count(capVisionAnnotations))
	}
}

// A star is vision's write, with the copy attached, and the acknowledged answer is written
// through to the cache. A failed write says so; it never pretends.
func TestAStarIsVisionsWriteAndWritesThrough(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	fv := &fakeVision{anns: map[string]any{}}
	a.vision = fv
	a.annotationsFor("reader@zm.is")
	waitLoaded(t, a, "reader@zm.is")
	post := func(form url.Values) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		a.quoteView().Mount(mux, a.webGate, a.page)
		mux.HandleFunc("/tag", a.serveTag)
		target := "/quotes/act"
		if form.Get("_to") != "" {
			target = form.Get("_to")
		}
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Remote-Email", "reader@zm.is")
		r.Header.Set("Remote-Groups", "plug_koreader")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post(url.Values{"action": {"star"}, "ref": {"koreader:a-p1"}, "on": {"1"}, "back": {"/quote?id=a-p1"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("star: HTTP %d %s", w.Code, w.Body.String())
	}
	if anns, _ := a.annotationsFor("reader@zm.is"); !anns["koreader:a-p1"].Starred {
		t.Error("the acknowledged star was not written through to the cache")
	}
	// Tag from anywhere: the day page's host route.
	if w := post(url.Values{"_to": {"/tag"}, "action": {"tag"}, "ref": {"koreader:a-p2"}, "prev": {""}, "tags": {"grit"}, "back": {"/day?d=2025-09-04"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/day?d=2025-09-04" {
		t.Fatalf("tag: HTTP %d to %q", w.Code, w.Header().Get("Location"))
	}
	if anns, _ := a.annotationsFor("reader@zm.is"); strings.Join(anns["koreader:a-p2"].Tags, ",") != "grit" {
		t.Error("the acknowledged tag was not written through")
	}
	fv.fail = fmt.Errorf("vision is down")
	if w := post(url.Values{"action": {"star"}, "ref": {"koreader:b-p9"}, "on": {"1"}}); w.Code != http.StatusBadGateway {
		t.Errorf("a failed star: HTTP %d, want 502", w.Code)
	}
	if anns, _ := a.annotationsFor("reader@zm.is"); anns["koreader:b-p9"].Starred {
		t.Error("a failed star was cached as starred")
	}
}

// Vision's event marks the cache stale: the next page still draws what is known (never
// "loading" right after your own star) and a refresh brings vision's latest behind it.
func TestVisionsEventRefreshesBehindThePage(t *testing.T) {
	a := testApp()
	fv := &fakeVision{anns: map[string]any{"koreader:x": map[string]any{"starred": true}}}
	a.vision = fv
	a.annotationsFor("reader@zm.is")
	waitLoaded(t, a, "reader@zm.is")
	fv.mu.Lock()
	fv.anns = map[string]any{"koreader:x": map[string]any{"starred": true}, "koreader:y": map[string]any{"starred": true}}
	fv.mu.Unlock()
	a.staleAnnotations("reader@zm.is")
	if anns, have := a.annotationsFor("reader@zm.is"); !have || !anns["koreader:x"].Starred {
		t.Fatal("a stale entry was not served while it refreshed")
	}
	for i := 0; i < 200; i++ {
		if anns, _ := a.annotationsFor("reader@zm.is"); anns["koreader:y"].Starred {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the refresh after vision's event never brought the new star")
}

// The highlight is never editable from koreader, whatever is posted.
func TestTheHighlightIsNeverEditedFromHere(t *testing.T) {
	a := testApp()
	if err := (koAnnotator{a}).Edit(context.Background(), "reader@zm.is", quotebrowser.Ref{Source: "koreader", ID: "x"}, "t", ""); err == nil {
		t.Error("koreader's annotator accepted an edit of a highlight")
	}
}

// The day page's quotes and the list's book names lead into the per-book reader
// (feedback 2cdd6c43, d3c929e9); the stat tiles use a house radius token (d9e112f4).
func TestQuotesLeadIntoTheirBooksReader(t *testing.T) {
	an := ReadEvent{ID: "a-p1", Kind: KindHighlight, BookMD5: "aaa", Title: "First", Page: 1, StartTime: 1757000100, Text: "one"}
	if got := string(annotationBlock(an, time.UTC, true)); !strings.Contains(got, `href="/quote?id=koreader%3Aa-p1&amp;book=aaa"`) {
		t.Errorf("the day page's quote does not open its book's reader: %s", got)
	}
	a := testApp()
	quoteFixture(t, a)
	if body := browse(t, a, "/quotes"); !strings.Contains(body, `href="/quote?book=aaa"`) {
		t.Error("the list's book name is not a link to that book's quotes")
	}
	if strings.Contains(koReadingCSS, "var(--radius)") {
		t.Error("var(--radius) is not a house token; it computes to 0 (square corners)")
	}
}

// Design §3, anti-drift 3: quote markup and styling live ONLY in the shared package. A
// ko-quote or qb- class written here would be this plug drawing a quote of its own.
func TestNoQuoteMarkupOutsideTheSharedBrowser(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	lit := regexp.MustCompile(`\bko-quote|["' .]qb-[a-z]`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if lit.MatchString(line) {
				t.Errorf("%s:%d draws quote markup of its own: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if !lit.MatchString(`<p class="ko-quote-text">`) || !lit.MatchString(`class="qb-text"`) {
		t.Error("the literal check no longer matches the markup it exists for")
	}
}

// The demo browses the persona's reading: the calendar has days, the browser has quotes,
// and a real tenant is never seeded.
func TestDemoSeedsThePersonasReading(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := dblib.TenantKey{Account: "demo-n1", Workspace: demo.WorkspacePrefix + "n1demo", Subject: "demo-n1demo"}
	idx, err := a.readingIndexFor(ctx, tk.Subject, tk)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Highlights()) != 8 || len(idx.Dates) < 20 {
		t.Fatalf("demo reading: %d highlights over %d days, want 8 over 20+", len(idx.Highlights()), len(idx.Dates))
	}
	if streak := currentStreak(idx, time.UTC); streak < 2 {
		t.Errorf("the demo streak is %d; the last two days are always read", streak)
	}
	// Idempotent: a second read writes nothing more.
	before, _ := a.events.List(ctx, tk)
	a.demoSeeded = sync.Map{}
	_, _ = a.readingIndexFor(ctx, tk.Subject, tk)
	after, _ := a.events.List(ctx, tk)
	if len(after) != len(before) {
		t.Errorf("a second read reseeded: %d rows became %d", len(before), len(after))
	}
	real := tenant("reader@zm.is")
	if _, err := a.readingIndexFor(ctx, "reader@zm.is", real); err != nil {
		t.Fatal(err)
	}
	if rows, _ := a.events.List(ctx, real); len(rows) != 0 {
		t.Errorf("a real tenant was seeded with %d demo rows", len(rows))
	}
}

// TestShelfCountsQuotesPerBook, including a book that has highlights but no reading rows —
// the statistics database and the sidecars are separate files and one can be pruned
// without the other, so dropping such a book would lose quotes that exist.
func TestShelfCountsQuotesPerBook(t *testing.T) {
	idx := buildReadingIndex([]ReadEvent{
		{ID: "r", Kind: KindRead, BookMD5: "aaa", Title: "First", Page: 1, StartTime: 1757000000, Duration: 600},
		{ID: "h1", Kind: KindHighlight, BookMD5: "aaa", Title: "First", StartTime: 1757000100, Text: "x"},
		{ID: "h2", Kind: KindHighlight, BookMD5: "zzz", Title: "Orphan", StartTime: 1757000200, Text: "y"},
		{ID: "m", Kind: KindBookmark, BookMD5: "aaa", Title: "First", StartTime: 1757000300},
	}, time.UTC)

	byTitle := map[string]BookShelf{}
	for _, b := range shelf(idx) {
		byTitle[b.Title] = b
	}
	if len(byTitle) != 2 {
		t.Fatalf("shelf has %d books, want 2: %+v", len(byTitle), byTitle)
	}
	if got := byTitle["First"]; got.Highlights != 1 || got.Bookmarks != 1 || got.Seconds != 600 {
		t.Errorf("First: %+v", got)
	}
	if got := byTitle["Orphan"]; got.Highlights != 1 {
		t.Errorf("a book with quotes but no reading rows was dropped: %+v", byTitle)
	}
}

// TestBookRowsOnlyLinkBooksThatHaveQuotes — a link into an empty reader is worse than none.
func TestBookRowsOnlyLinkBooksThatHaveQuotes(t *testing.T) {
	rows, total := bookRows([]BookShelf{
		{MD5: "aaa", Title: "Has quotes", Highlights: 3},
		{MD5: "bbb", Title: "No quotes", Highlights: 0, Seconds: 900},
	}, webui.ListQuery{Page: 1, PageSize: 10})

	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if !strings.Contains(string(rows[0]["title"]), `href="/quote?book=aaa"`) {
		t.Errorf("the book with quotes is not linked: %s", rows[0]["title"])
	}
	if strings.Contains(string(rows[1]["title"]), "href") {
		t.Errorf("a book with no quotes must not link into the reader: %s", rows[1]["title"])
	}
}

func TestHumanDurationPrefersHours(t *testing.T) {
	for _, tc := range []struct {
		secs int
		want string
	}{{0, "—"}, {30, "1 min"}, {600, "10 min"}, {3600, "1.0 h"}, {19800, "5.5 h"}} {
		if got := humanDuration(tc.secs); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}
