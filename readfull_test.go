package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The reader has a Full screen button in its bar and a strip of Previous / Next / exit that is only drawn
// in full screen; the page's own CSS hides the header, nav, footer and dock and the bar.
func TestTheReaderHasAFullScreenMode(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	if err := a.sources.Put(ctx, tk, sourceDocID, &Source{ID: sourceDocID, URL: "https://books.example.com"}); err != nil {
		t.Fatal(err)
	}
	e := CatalogEntry{ID: "urn:x:pdf", Title: "Be Here Now", Files: []BookFile{{Href: "https://books.example.com/d/9", Type: "application/pdf"}}}
	if err := a.saveCatalog(ctx, tk, &Catalog{ID: catalogID, SourceURL: "https://books.example.com", RefreshedAt: nowRFC(), Entries: []CatalogEntry{e}}); err != nil {
		t.Fatal(err)
	}
	if err := a.files.store(a.files.path(tk, entryKey(e.ID)+"-0.pdf"), strings.NewReader("%PDF-1.4\n"+strings.Repeat("x", 3000))); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+entryKey(e.ID), ""))
	b, _ := io.ReadAll(rec.Body)
	page := string(b)
	for _, want := range []string{`id="rd-full"`, `data-rd-full="toggle"`, fullScreen, `id="rd-fs"`, `data-rd-nav="prev"`, `data-rd-nav="next"`, `data-rd-full="exit"`,
		fullScreenExit, "requestFullscreen", "fullscreenchange", "Escape"} {
		if !strings.Contains(page, want) {
			t.Errorf("the reader page lacks %q", want)
		}
	}
	for _, want := range []string{`html.rd-full body>*:not(main){display:none!important}`, `html.rd-full .rd-bar`, `.rd-fs.on{opacity:1}`} {
		if !strings.Contains(readerFullCSS, want) {
			t.Errorf("the full screen CSS lacks %q", want)
		}
	}
	// The strip sits in the shell, so a chapter swap redraws it; the one script that drives it sits outside, once.
	if strings.Count(page, "window.__rdFull") != 2 { // the guard's test and its set
		t.Error("the full screen script is not loaded once, guarded")
	}
	// The mode is on <html>, never on #rd-shell, which a chapter swap replaces.
	if strings.Contains(readerFullJS, "rd-shell") {
		t.Error("full screen must not hang on #rd-shell")
	}
}
