package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(string(read[0]["quotes"]), "/quote?book=m1") || strings.Contains(string(read[0]["title"]), "href") || !strings.Contains(string(read[0]["formats"]), "> Read<") {
		t.Errorf("a book read AND on the server shows both sides: %v", read[0])
	}
	if strings.Contains(string(read[1]["formats"]), "btn") {
		t.Errorf("a book not on the server has no buttons: %v", read[1])
	}
	if sw := string(librarySwitch(libShowRead)); !strings.Contains(sw, "show=read") || !strings.Contains(sw, `hx-target="#lv-books"`) {
		t.Errorf("the filter must be the house switch swapping the list: %s", sw)
	}
}

// Owner via master, plan 26856ccd: a title the feed escaped twice printed as "&#40;Kate Daniels 1&#41;".
func TestLibraryTitleIsEscapedOnce(t *testing.T) {
	e := CatalogEntry{ID: "u", Title: "&#40;Kate Daniels 1&#41;", Authors: "Andrews &amp; Ilona"}
	rows, _ := libraryRows(buildLibrary([]CatalogEntry{e}, nil), webui.ListQuery{Page: 1, PageSize: 25}, false)
	if got := string(rows[0]["title"]); !strings.Contains(got, ">(Kate Daniels 1)<") || strings.Contains(got, "&amp;#") {
		t.Errorf("title = %q", got)
	}
	if got := string(rows[0]["authors"]); got != "Andrews &amp; Ilona" {
		t.Errorf("authors = %q (one layer of escaping, for the page)", got)
	}
}

// A reading with no title says so, rather than "Untitled" beside a stranger.
func TestLibraryUnnamedReadingIsExplained(t *testing.T) {
	rows, _ := libraryRows(buildLibrary(nil, []BookShelf{{MD5: "z", Title: "", Authors: "human", Seconds: 600}}), webui.ListQuery{Page: 1, PageSize: 25}, false)
	if got := string(rows[0]["title"]); !strings.Contains(got, libUnnamed) || !strings.Contains(got, "data-tip") || strings.Contains(got, "Untitled") {
		t.Errorf("title cell = %s", got)
	}
}

// One statement: "Updated 20 h ago" with a spinner, never "Updating… Updated 20 h ago".
func TestCatalogStatusIsOneStatement(t *testing.T) {
	c := &Catalog{RefreshedAt: time.Now().Add(-20 * time.Hour).Format(time.RFC3339)}
	got := catalogStatus(c, true)
	if strings.Contains(got, "Updating") || !strings.Contains(got, "Updated 20 h ago") || !strings.Contains(got, "fa-spin") {
		t.Errorf("status = %s", got)
	}
	if got := catalogStatus(&Catalog{}, true); !strings.Contains(got, "Updating") {
		t.Errorf("with no earlier update, say Updating: %s", got)
	}
}

// The Read and download buttons stand side by side in one house ButtonGroup that does not wrap.
func TestLibraryActionsAreOneButtonGroup(t *testing.T) {
	e := CatalogEntry{ID: "u", Title: "Dune", Files: []BookFile{{Type: "application/epub+zip", Href: "/x"}}}
	rows, _ := libraryRows(buildLibrary([]CatalogEntry{e}, nil), webui.ListQuery{Page: 1, PageSize: 25}, false)
	h := string(rows[0]["formats"])
	if !strings.Contains(h, `class="rd-acts"`) || !strings.Contains(h, `class="btn-group"`) || strings.Count(h, `class="btn `) != 2 {
		t.Errorf("actions = %s", h)
	}
}

// The entities are undone until the text stops changing: "&amp;#40;" (escaped twice) becomes "(".
func TestLibraryTitleDoubleEscapedByTheFeed(t *testing.T) {
	e := CatalogEntry{ID: "u", Title: "&amp;#40;Kate Daniels 1&amp;#41; Andrews, Ilona - Magic Bites"}
	rows, _ := libraryRows(buildLibrary([]CatalogEntry{e}, nil), webui.ListQuery{Page: 1, PageSize: 25}, false)
	got := string(rows[0]["title"])
	if strings.Contains(got, "#40") || strings.Contains(got, "#41") || !strings.Contains(got, ">(Kate Daniels 1) Andrews") {
		t.Errorf("title = %s", got)
	}
	if unescapeOnce("a &lt; b") != "a < b" || unescapeOnce("plain") != "plain" {
		t.Error("single layer and plain text")
	}
}

// A long title is clamped to two lines (full text is the tooltip) and the action cell fits its content.
func TestLibraryRowsStayEvenHeight(t *testing.T) {
	rows, _ := libraryRows(buildLibrary([]CatalogEntry{{ID: "u", Title: strings.Repeat("Long ", 30)}}, nil), webui.ListQuery{Page: 1, PageSize: 25}, false)
	if got := string(rows[0]["title"]); !strings.Contains(got, `class="rd-title" title="Long Long`) {
		t.Errorf("title = %s", got)
	}
	for _, want := range []string{"-webkit-line-clamp:2", "td[data-k=formats]{width:1%;white-space:nowrap;text-align:right}"} {
		if !strings.Contains(readerBooksCSS, want) {
			t.Errorf("CSS lacks %s", want)
		}
	}
}

// The same fixed-point unescape runs on what the DEVICE sent: a reading-session title with no catalog
// match, the calendar's bars, the day page and the quote export all print it once-escaped.
func TestReadingTitlesAreUnescapedEverywhere(t *testing.T) {
	const raw = "&amp;#40;Kate Daniels 1&amp;#41; Andrews, Ilona - Magic Bites"
	idx := buildReadingIndex([]ReadEvent{
		{ID: "r1", Kind: KindRead, BookMD5: "k", Title: raw, Authors: "Andrews &amp;amp; Ilona", StartTime: 1789000000, Duration: 600, Page: 1},
		{ID: "h1", Kind: KindHighlight, BookMD5: "k", Title: raw, StartTime: 1789000100, Text: "a line"},
	}, time.UTC)
	rows, _ := libraryRows(buildLibrary(nil, shelf(idx)), webui.ListQuery{Page: 1, PageSize: 25}, true)
	got := string(rows[0]["title"])
	if strings.Contains(got, "#40") || strings.Contains(got, "&amp;") || !strings.Contains(got, ">(Kate Daniels 1) Andrews, Ilona - Magic Bites<") {
		t.Errorf("shelf title = %s", got)
	}
	if a := string(rows[0]["authors"]); a != "Andrews &amp; Ilona" {
		t.Errorf("authors = %q", a)
	}
	if d := displayTitle(raw); strings.Contains(d, "#40") {
		t.Errorf("displayTitle = %q", d)
	}
	for _, an := range idx.Annotations {
		if strings.Contains(an.Title, "#40") {
			t.Errorf("annotation title = %q", an.Title)
		}
	}
}

// ROW BUTTONS ARE ONE WIDTH. Read, PDF, EPUB and BIN differed by the length of their label
// (76.5, 86.7 and 89.7 px on the owner's list), so the buttons of different rows did not line
// up and a few pixels of ragged edge showed (owner, plan 26856ccd). Every button in a row's
// actions takes the same minimum width, wide enough for the longest label.
func TestEveryRowButtonIsTheSameWidth(t *testing.T) {
	if !strings.Contains(readerBooksCSS, ".rd-acts .btn{white-space:nowrap;min-width:var(--rd-act-w)}") ||
		!strings.Contains(readerBooksCSS, ".rd-acts{--rd-act-w:5.75rem}") {
		t.Error("the row buttons are not given one width")
	}
}
