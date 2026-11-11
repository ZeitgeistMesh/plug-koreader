package main

// progress.go — where a person is in each book, kept in one place so the browser and a
// KOReader device agree.
//
// A position is a percentage of the book plus, for the browser, the chapter and how far into
// it. KOReader knows only the percentage, so that is the value both sides share. The newest
// change wins: every position carries the time it was made, a device sends its own on every
// sync and is told about any that the server holds from a later time.
//
// A book is named by KOReader's partial md5 of its file, which a device computes on its own
// and this plug computes from the file it fetched for the browser; no id is invented.

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"xf/controller/pkg/agentprivacy"
	"xf/controller/pkg/dblib"
	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/xats"
)

const (
	collProgress = "reader_progress"

	capProgressGet    = "reader.progress.get"
	capProgressSet    = "reader.progress.set"
	capHighlightAdd   = "reader.highlight.add"
	webDeviceID       = "web"
	webDeviceName     = "browser"
	maxHighlightBytes = 4000
)

// Progress is a person's place in one book.
type Progress struct {
	ID        string  `json:"id"` // the book's partial md5
	Title     string  `json:"title,omitempty"`
	Percent   float64 `json:"percent"`
	Chapter   int     `json:"chapter,omitempty"`
	Frac      float64 `json:"frac,omitempty"`
	Device    string  `json:"device,omitempty"` // "browser" or the name of an e-reader
	At        int64   `json:"at"`               // Unix seconds the position was made
	Subject   string  `json:"subject,omitempty"`
	Workspace string  `json:"workspace,omitempty"`
}

// koPartialMD5 is KOReader's identifier for a file: the md5 of up to 1024 bytes read at offset
// 0 and at 1024<<(2*i) for i from 0 to 10, stopping at the first read that returns nothing.
func koPartialMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	buf := make([]byte, 1024)
	for i := -1; i <= 10; i++ {
		off := int64(0)
		if i >= 0 {
			off = int64(1024) << (2 * uint(i))
		}
		n, err := f.ReadAt(buf, off)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			return "", err
		}
		if n == 0 {
			break
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func (a *app) loadProgress(ctx context.Context, tk dblib.TenantKey, book string) *Progress {
	if a.progress == nil || book == "" {
		return nil
	}
	p, err := a.progress.Get(ctx, tk, book)
	if err != nil || p == nil {
		return nil
	}
	return p
}

// savePosition stores a position when it is newer than the one held (or when force is set, for
// a person's own explicit change). It reports whether it was stored.
func (a *app) savePosition(ctx context.Context, tk dblib.TenantKey, p Progress, force bool) (bool, error) {
	if a.progress == nil || p.ID == "" {
		return false, nil
	}
	p.Percent = min(max(p.Percent, 0), 100)
	if p.At <= 0 {
		p.At = time.Now().Unix()
	}
	if old := a.loadProgress(ctx, tk, p.ID); old != nil {
		if !force && old.At >= p.At {
			return false, nil
		}
		if p.Title == "" {
			p.Title = old.Title
		}
	}
	p.Subject, p.Workspace = tk.Subject, tk.Workspace
	return true, a.progress.Put(ctx, tk, p.ID, &p)
}

// devicePosition travels both ways on a sync. From a device: the percentage it is at, the time
// of its last agreement with the server (or of its own change, when Changed is set). From the
// server: a newer position from another place, with the name of where it was made.
type devicePosition struct {
	Book    string  `json:"book"`
	Percent float64 `json:"percent"`
	At      int64   `json:"at"`
	Changed bool    `json:"changed,omitempty"`
	Device  string  `json:"device,omitempty"`
}

// syncPosition takes the device's position and answers with the server's when that is newer
// and was made elsewhere. A device that did not move sends Changed false, so opening a book
// never overwrites a position made in the browser.
func (a *app) syncPosition(ctx context.Context, tk dblib.TenantKey, dev Device, in *devicePosition) *devicePosition {
	if in == nil || len(in.Book) != 32 {
		return nil
	}
	if in.Changed {
		_, _ = a.savePosition(ctx, tk, Progress{ID: in.Book, Percent: in.Percent, Device: dev.Name, At: in.At}, false)
	}
	cur := a.loadProgress(ctx, tk, in.Book)
	if cur == nil || cur.At <= in.At || cur.Device == dev.Name || math.Abs(cur.Percent-in.Percent) < 1 {
		return nil
	}
	return &devicePosition{Book: cur.ID, Percent: cur.Percent, At: cur.At, Device: cur.Device}
}

func progressView(p *Progress) map[string]any {
	if p == nil {
		return map[string]any{"found": false}
	}
	return map[string]any{"found": true, "book": p.ID, "percent": p.Percent, "chapter": p.Chapter, "frac": p.Frac, "device": p.Device, "at": p.At}
}

func (a *app) handleProgressGet(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	return progressView(a.loadProgress(ctx, tk, strings.TrimSpace(plugrun.Str(meta, "book"))))
}

func (a *app) handleProgressSet(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	book := strings.TrimSpace(plugrun.Str(meta, "book"))
	if len(book) != 32 {
		return errMap(ui.ErrNoChapter)
	}
	p := Progress{ID: book, Title: plugrun.Str(meta, "title"), Device: webDeviceName, At: time.Now().Unix()}
	p.Percent, _ = meta["percent"].(float64)
	if n, ok := meta["chapter"].(float64); ok && n >= 0 {
		p.Chapter = int(n)
	}
	p.Frac, _ = meta["frac"].(float64)
	if _, err := a.savePosition(ctx, tk, p, true); err != nil {
		return errMap(ui.ErrSave)
	}
	return progressView(a.loadProgress(ctx, tk, book))
}

// handleHighlightAdd stores a passage selected in the browser the way a device's highlight is
// stored, so it reaches vision as a quote by the same road.
func (a *app) handleHighlightAdd(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	book := strings.TrimSpace(plugrun.Str(meta, "book"))
	text := strings.TrimSpace(plugrun.Str(meta, "text"))
	if len(book) != 32 || text == "" {
		return errMap(ui.ErrNoChapter)
	}
	if len(text) > maxHighlightBytes {
		text = text[:maxHighlightBytes]
	}
	now := time.Now().Unix()
	chapter := plugrun.Str(meta, "chapter")
	ev := ReadEvent{
		ID:   "web-" + fmt.Sprintf("%x", md5.Sum([]byte(book+"\x00"+chapter+"\x00"+text)))[:24],
		Kind: KindHighlight, BookMD5: book, Title: plugrun.Str(meta, "title"), Authors: plugrun.Str(meta, "authors"),
		StartTime: now, Text: text, Note: plugrun.Str(meta, "note"), Chapter: chapter,
	}
	rep := a.storeEvents(ctx, tk, Device{ID: webDeviceID, Name: webDeviceName, Subject: tk.Subject, Workspace: tk.Workspace}, []ReadEvent{ev})
	if rep.Rejected > 0 {
		return errMap(ui.ErrSave)
	}
	a.index.bump(caller)
	return map[string]any{"ok": true, "saved": rep.Accepted, "body": "Saved."}
}

func progressCapabilities() []xats.Capability {
	sec := []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}}
	mk := func(name, title, desc, icon string, ro bool, props map[string]any) xats.Capability {
		c := xats.Capability{
			Name: name, Command: name, Title: title, Description: desc, Icon: icon,
			RequiredScopes: []string{scopeUser}, Security: sec, ExposeToMCP: false,
			InputSchema: map[string]any{"type": "object", "properties": props},
		}
		if ro {
			c.Annotations = map[string]any{"readOnlyHint": true}
		}
		return c
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	num := func(d string) map[string]any { return map[string]any{"type": "number", "description": d} }
	return []xats.Capability{
		mk(capProgressGet, "Reading Position", "Where you are in a book: how far through it, and on which device you were last.",
			"fa-solid fa-bookmark", true, map[string]any{"book": str("the book's id, the md5 KOReader uses")}),
		mk(capProgressSet, "Save Reading Position", "Save where you are in a book. Your e-reader is told at its next sync.",
			"fa-solid fa-bookmark", false, map[string]any{
				"book": str("the book's id, the md5 KOReader uses"), "percent": num("how far through the whole book, 0 to 100"),
				"chapter": num("the chapter number, from 0"), "frac": num("how far through that chapter, 0 to 1"), "title": str("the book's title")}),
		mk(capHighlightAdd, "Save a Highlight", "Save a passage you selected while reading in the browser. It becomes a quote like a highlight made on your e-reader.",
			"fa-solid fa-highlighter", false, map[string]any{
				"book": str("the book's id, the md5 KOReader uses"), "text": str("the selected passage"), "note": str("your note on it"),
				"chapter": str("the chapter's title"), "title": str("the book's title"), "authors": str("the book's authors")}),
	}
}
