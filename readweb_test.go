package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func readerReq(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_reader")
	r.Header.Set("Origin", "https://reader.zm.is")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// A connected person with one EPUB in the catalog and its file already cached: the page opens
// the chapter, serves the book's own images and nothing else, and the two POST routes keep the
// place and the highlight.
func TestTheReaderPageOpensTheBookAndSavesPlaceAndHighlights(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	if err := a.sources.Put(ctx, tk, sourceDocID, &Source{ID: sourceDocID, URL: "https://books.example.com"}); err != nil {
		t.Fatal(err)
	}
	e := CatalogEntry{ID: "urn:x:1", Title: "A Small Book", Authors: "Someone", Files: []BookFile{{Href: "https://books.example.com/d/1", Type: "application/epub+zip"}}}
	if err := a.saveCatalog(ctx, tk, &Catalog{ID: catalogID, SourceURL: "https://books.example.com", RefreshedAt: nowRFC(), Entries: []CatalogEntry{e}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(makeEpub(t))
	if err := a.files.store(a.files.path(tk, entryKey(e.ID)+"-0.epub"), strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	key := entryKey(e.ID)

	rec := httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+key, ""))
	page, _ := io.ReadAll(rec.Body)
	for _, want := range []string{"The Start", `id="rd-text"`, "Hello &amp;", ui.ContentsHeading, ui.Next, `data-book="`, "/books/read/res?id=" + key} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// Chapters swap in place (htmx), the bar is at the top only, and the reading area scrolls inside the page.
	for _, want := range []string{`id="rd-shell"`, `hx-get="/books/read?id=` + key + `&amp;n=1"`, `hx-select="#rd-shell"`, `hx-push-url="true"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the chapter links do not swap in place: lacks %q", want)
		}
	}
	shell := string(page)[strings.Index(string(page), `id="rd-shell"`):]
	if strings.Count(shell, `hx-boost="false"`) != strings.Count(shell, `target="_blank"`) || strings.Count(shell, `class="rd-bar"`) != 1 {
		t.Errorf("a full page load on prev/next, or a second bar under the text (%d)", strings.Count(shell, `class="rd-bar"`))
	}
	if strings.Contains(string(page), "alert(") && !strings.Contains(string(page), "<script>\n(function(){") {
		t.Error("book script reached the page")
	}
	if strings.Contains(strings.ReplaceAll(string(page), readerReadJS, ""), "alert(") {
		t.Error("a script from the book reached the page")
	}

	rec = httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+key+"&n=9", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a chapter past the end answered %d", rec.Code)
	}

	// Images: the manifest's own, and nothing else.
	rec = httptest.NewRecorder()
	a.serveBookRes(rec, readerReq(http.MethodGet, booksPath+"/read/res?id="+key+"&p=OEBPS/img/pic.png", ""))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("image: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"OEBPS/content.opf", "META-INF/container.xml", "../../etc/passwd"} {
		rec = httptest.NewRecorder()
		a.serveBookRes(rec, readerReq(http.MethodGet, booksPath+"/read/res?id="+key+"&p="+p, ""))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d", p, rec.Code)
		}
	}

	// The place is kept, and the next visit opens there.
	md5, _ := koPartialMD5(a.files.path(tk, key+"-0.epub"))
	rec = httptest.NewRecorder()
	a.serveReadProgress(rec, readerReq(http.MethodPost, booksPath+"/read/progress", `{"book":"`+md5+`","chapter":1,"frac":0.4,"percent":70,"title":"A Small Book"}`))
	if rec.Code != 200 {
		t.Fatalf("progress answered %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+key, ""))
	if got := rec.Body.String(); !strings.Contains(got, "The end.") || !strings.Contains(got, `data-frac="0.4000"`) {
		t.Errorf("the book did not open where it was left:\n%s", got)
	}

	// A position made on a device is only a percentage: it opens in the right chapter.
	_, _ = a.savePosition(ctx, tk, Progress{ID: md5, Percent: 1, Device: "Kobo", At: 9999999999}, false)
	rec = httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+key, ""))
	if got := rec.Body.String(); !strings.Contains(got, "Hello &amp;") {
		t.Errorf("a device position did not open the first chapter:\n%s", got)
	}

	rec = httptest.NewRecorder()
	a.serveReadHighlight(rec, readerReq(http.MethodPost, booksPath+"/read/highlight", `{"book":"`+md5+`","text":"Hello friend","chapter":"The Start","title":"A Small Book"}`))
	if rec.Code != 200 {
		t.Fatalf("highlight answered %d: %s", rec.Code, rec.Body.String())
	}
	if evs, _ := a.events.List(ctx, tk); len(evs) != 1 || evs[0].Text != "Hello friend" {
		t.Errorf("events = %+v", evs)
	}

	// Another person's catalog is not this one's.
	rec = httptest.NewRecorder()
	r := readerReq(http.MethodGet, booksPath+"/read?id="+key, "")
	r.Header.Set("Remote-Email", "other@zm.is")
	a.serveBookRead(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("another person got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.serveReadProgress(rec, readerReq(http.MethodGet, booksPath+"/read/progress", ""))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET to the progress route answered %d", rec.Code)
	}
}
