package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The reader's bar has a Maximise button (owner, 26856ccd cdeef813: the windowed view); the strip of Previous / Full
// screen / Restore / Next is only drawn once maximised, and its Full screen button is the true full screen. The page's
// own CSS hides the header, nav, footer and dock and the bar.
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
	for _, want := range []string{`id="rd-full"`, `data-rd-full="toggle"`, maximise, `id="rd-fs"`, `data-rd-nav="prev"`, `data-rd-nav="next"`, `data-rd-full="exit"`,
		`data-rd-full="enterfs"`, `data-rd-full="leavefs"`, fullScreen, fullScreenExit, maximiseRestore, "requestFullscreen", "fullscreenchange", "Escape"} {
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

// Owner (26856ccd cdeef813): Maximise is the windowed view and does NOT ask the browser for full screen; only the
// Full screen button inside it does. Leaving true full screen lands back on the maximised view, and Restore (or Esc
// in the window) leaves Maximise.
func TestMaximiseIsWindowedAndFullScreenIsItsOwnStep(t *testing.T) {
	js := readerFullJS
	if !strings.Contains(js, "else{set(true);}") {
		t.Error("the bar's Maximise must only set the class")
	}
	if strings.Count(js, "requestFullscreen()") != 1 || !strings.Contains(js, "k==='enterfs'){set(true);enterFs();}") {
		t.Error("only the strip's Full screen button may ask the browser for full screen")
	}
	if strings.Contains(js, "fullscreenchange',function(){if(!document.fullscreenElement&&on())set(false)") {
		t.Error("leaving true full screen must not leave the maximised view")
	}
	if !strings.Contains(js, "toggle('rd-true',!!document.fullscreenElement)") {
		t.Error("the true full screen state must follow the browser's own")
	}
	if !strings.Contains(fullButton(), maximise) {
		t.Error("the bar button is called Maximise")
	}
	if strings.Contains(fullButton(), fullScreen) {
		t.Error("the bar button no longer says Full screen")
	}
	strip := fullStrip()
	for _, want := range []string{"rd-fs-enter", "rd-fs-leave", `data-rd-full="exit"`, maximiseRestore} {
		if !strings.Contains(strip, want) {
			t.Errorf("the strip lacks %q", want)
		}
	}
}
