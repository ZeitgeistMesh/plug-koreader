package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"xf/controller/pkg/webui"
)

// Owner, plan 26856ccd: Read is a filter inside Books, not a second page and not a nav entry.
func TestReadIsAFilterOnBooksNotANavEntry(t *testing.T) {
	for _, l := range nav("/") {
		if l.Label == "Read" || l.URL == readingPath {
			t.Errorf("the nav still offers %q -> %s", l.Label, l.URL)
		}
	}
	w := httptest.NewRecorder()
	serveReadingMoved(w, httptest.NewRequest("GET", "/reading", nil))
	if w.Code != 301 || w.Header().Get("Location") != "/books?show=read" {
		t.Errorf("/reading: HTTP %d to %q", w.Code, w.Header().Get("Location"))
	}
}

// One list, one column set: the whole list and the Read filter differ only in which rows they hold.
func TestLibraryFilterKeepsOnlyReadBooksAndTheSameColumns(t *testing.T) {
	epub := []BookFile{{Type: "application/epub+zip", Href: "/x.epub"}}
	entries := []CatalogEntry{
		{ID: "u1", Title: "Dune", Authors: "Herbert", Files: epub},
		{ID: "u2", Title: "Emma", Authors: "Austen", Files: epub},
	}
	shelves := []BookShelf{{MD5: "m1", Title: "dune", Seconds: 1800, Pages: 12, Days: 3, Highlights: 2}, {MD5: "m2", Title: "Off Server", Seconds: 60}}
	books := buildLibrary(entries, shelves)
	q := webui.ListQuery{Page: 1, PageSize: 25}
	all, nAll := libraryRows(books, q, false)
	read, nRead := libraryRows(books, q, true)
	if nAll != 3 || nRead != 2 {
		t.Fatalf("all=%d read=%d, want 3 and 2", nAll, nRead)
	}
	for _, rows := range [][]webui.Row{all, read} {
		for _, r := range rows {
			for _, c := range libraryListView().Columns {
				if _, ok := r[c.Key]; !ok {
					t.Errorf("row lacks column %q", c.Key)
				}
			}
		}
	}
	if !strings.Contains(string(read[0]["title"]), "/quote?book=m1") || !strings.Contains(string(read[0]["formats"]), "> Read<") {
		t.Errorf("a book read AND on the server shows both sides: %v", read[0])
	}
	if strings.Contains(string(read[1]["formats"]), "btn") {
		t.Errorf("a book not on the server has no buttons: %v", read[1])
	}
	if sw := string(librarySwitch(libShowRead)); !strings.Contains(sw, "show=read") || !strings.Contains(sw, `hx-target="#lv-books"`) {
		t.Errorf("the filter must be the house switch swapping the list: %s", sw)
	}
}
