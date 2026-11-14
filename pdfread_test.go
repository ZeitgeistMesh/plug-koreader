package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A book that is only a PDF opens in the same reader page as an EPUB (owner, plan 26856ccd): the same
// shell, bar, contents, position and save-a-quote button, with the pages drawn by pdf.js.
func TestAPDFOpensInTheSameReaderPage(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	if err := a.sources.Put(ctx, tk, sourceDocID, &Source{ID: sourceDocID, URL: "https://books.example.com"}); err != nil {
		t.Fatal(err)
	}
	e := CatalogEntry{ID: "urn:x:pdf", Title: "Be Here Now", Authors: "Someone", Files: []BookFile{{Href: "https://books.example.com/d/9", Type: "application/pdf"}}}
	if err := a.saveCatalog(ctx, tk, &Catalog{ID: catalogID, SourceURL: "https://books.example.com", RefreshedAt: nowRFC(), Entries: []CatalogEntry{e}}); err != nil {
		t.Fatal(err)
	}
	pdf := "%PDF-1.4\n" + strings.Repeat("x", 3000)
	if err := a.files.store(a.files.path(tk, entryKey(e.ID)+"-0.pdf"), strings.NewReader(pdf)); err != nil {
		t.Fatal(err)
	}
	key := entryKey(e.ID)

	rec := httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+key, ""))
	body, _ := io.ReadAll(rec.Body)
	page := string(body)
	for _, want := range []string{
		`id="rd-shell"`, `class="rd-bar"`, `id="rd-prev"`, `id="rd-next"`, `id="rd-pos"`, `id="rd-toc"`, `id="rd-text"`, `id="rd-quote"`,
		`data-book="`, `data-progress-url="/books/read/progress"`, `data-highlight-url="/books/read/highlight"`,
		`data-file-url="/books/file?id=` + key + `&amp;f=0"`, "pdf.js/" + pdfJSVersion + "/pdf.min.js", ui.PdfCantDraw, ui.ContentsHeading, ui.Prev, ui.Next,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the PDF reader page lacks %q", want)
		}
	}
	// One pinned script from cdnjs and nothing else outside the origin.
	if n := strings.Count(page, "https://cdnjs.cloudflare.com/"); n != 2 { // the script and the worker
		t.Errorf("cdnjs is named %d times, want the script and the worker only", n)
	}

	// The library offers Read for it, to the same address an EPUB has.
	row := libraryRow(libBook{Entry: &e})
	if h := string(row["formats"]); !strings.Contains(h, booksPath+"/read?id="+key) {
		t.Errorf("the PDF row has no Read button: %s", h)
	}
	// A quoted PDF book finds its reader too.
	if readableFile(&CatalogEntry{Files: []BookFile{{Type: "application/x-mobipocket-ebook"}}}) {
		t.Error("a format the reader cannot draw must not offer Read")
	}
}

// A place a device made is a percentage; the page keeps it for the script, which turns it into a page.
func TestAPDFPlaceFromADeviceIsHandedToThePageAsAPercentage(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	_ = a.sources.Put(ctx, tk, sourceDocID, &Source{ID: sourceDocID, URL: "https://books.example.com"})
	e := CatalogEntry{ID: "urn:x:pdf2", Title: "Another", Files: []BookFile{{Href: "https://books.example.com/d/8", Type: "application/pdf"}}}
	_ = a.saveCatalog(ctx, tk, &Catalog{ID: catalogID, SourceURL: "https://books.example.com", RefreshedAt: nowRFC(), Entries: []CatalogEntry{e}})
	pdf := "%PDF-1.4\n" + strings.Repeat("y", 3000)
	_ = a.files.store(a.files.path(tk, entryKey(e.ID)+"-0.pdf"), strings.NewReader(pdf))
	md5, err := koPartialMD5(a.files.path(tk, entryKey(e.ID)+"-0.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.savePosition(ctx, tk, Progress{ID: md5, Percent: 42.5, Device: "Kobo", At: 1}, false); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.serveBookRead(rec, readerReq(http.MethodGet, booksPath+"/read?id="+entryKey(e.ID), ""))
	page, _ := io.ReadAll(rec.Body)
	for _, want := range []string{`data-page="-1"`, `data-percent="42.50"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("a device's place is not handed over: lacks %q", want)
		}
	}
}
