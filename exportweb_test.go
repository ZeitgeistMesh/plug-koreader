package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBooksExportCarriesNoText pins the promise /books.json is built on: metadata and
// counts per book, and not one byte of what the reader highlighted, noted or bookmarked.
func TestBooksExportCarriesNoText(t *testing.T) {
	loc := time.UTC
	day1 := time.Date(2026, 9, 29, 21, 0, 0, 0, loc).Unix()
	day2 := time.Date(2026, 9, 30, 22, 0, 0, 0, loc).Unix()
	events := []ReadEvent{
		{ID: "r1", Kind: KindRead, BookMD5: "edge", Title: "Unknown - The Slight Edge", Pages: 218, Page: 15, StartTime: day1, Duration: 60},
		{ID: "r2", Kind: KindRead, BookMD5: "edge", Title: "Unknown - The Slight Edge", Pages: 218, Page: 16, StartTime: day1 + 60, Duration: 90},
		{ID: "r3", Kind: KindRead, BookMD5: "edge", Title: "Unknown - The Slight Edge", Pages: 218, Page: 16, StartTime: day2, Duration: 30},
		{ID: "r4", Kind: KindRead, BookMD5: "old", Title: "Deep Work", Authors: "Cal Newport", Pages: 300, Page: 1, StartTime: day1 - 86400*30, Duration: 120},
		{ID: "h1", Kind: KindHighlight, BookMD5: "edge", StartTime: day2, Text: "SECRET-HIGHLIGHT", Note: "SECRET-NOTE", Chapter: "SECRET-CHAPTER"},
		{ID: "b1", Kind: KindBookmark, BookMD5: "edge", StartTime: day2, Text: "SECRET-CAPTURED-PAGE"},
		{ID: "b2", Kind: KindBookmark, BookMD5: "edge", StartTime: day2},
		{ID: "h2", Kind: KindHighlight, BookMD5: "only-quotes", Title: "Annotated only", StartTime: day1, Text: "SECRET-TOO"},
	}
	idx := buildReadingIndex(events, loc)
	devs := []Device{{Name: "Kobo", DeviceModel: "Kobo Libra", TokenSHA256: "SECRET-HASH", TokenTail: "SECRET-TAIL"}}
	out := buildBooksExport(idx, devs, loc, time.Unix(day2+3600, 0))

	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("the export leaked text or a token: %s", raw)
	}

	if len(out.Books) != 3 || out.Books[0].MD5 != "edge" || out.Books[2].MD5 != "old" {
		t.Fatalf("books not most-recent-first: %+v", out.Books)
	}
	e := out.Books[0]
	want := exportBook{MD5: "edge", Title: "Unknown - The Slight Edge", DisplayTitle: "The Slight Edge",
		BookPages: 218, Seconds: 180, PagesRead: 3, DistinctPages: 2, PageTurns: 3, DaysRead: 2,
		Highlights: 1, Bookmarks: 2, BookmarksWithPage: 1, Notes: 1, Quotes: 2}
	if e.Title != want.Title || e.DisplayTitle != want.DisplayTitle || e.BookPages != want.BookPages ||
		e.Seconds != want.Seconds || e.PagesRead != want.PagesRead || e.DistinctPages != want.DistinctPages ||
		e.PageTurns != want.PageTurns || e.DaysRead != want.DaysRead || e.Highlights != want.Highlights ||
		e.Bookmarks != want.Bookmarks || e.BookmarksWithPage != want.BookmarksWithPage ||
		e.Notes != want.Notes || e.Quotes != want.Quotes {
		t.Fatalf("Slight Edge entry\n got %+v\nwant %+v", e, want)
	}
	if e.FirstRead != "2026-09-29T21:00:00Z" || e.LastRead != "2026-09-30T22:00:30Z" {
		t.Fatalf("first/last read = %s / %s", e.FirstRead, e.LastRead)
	}
	if len(e.Days) != 2 || e.Days[0] != (exportDay{"2026-09-29", 150, 2}) || e.Days[1] != (exportDay{"2026-09-30", 30, 1}) {
		t.Fatalf("days = %+v", e.Days)
	}
	if q := out.Books[1]; q.MD5 != "only-quotes" || q.Title != "Annotated only" || q.Seconds != 0 || q.Days == nil {
		t.Fatalf("a book with quotes and no reading rows must still be listed, with an empty days list: %+v", q)
	}
	if out.Totals.Books != 3 || out.Totals.Seconds != 300 || out.Totals.PageTurns != 4 || out.Totals.DaysRead != 3 {
		t.Fatalf("totals = %+v", out.Totals)
	}
}

// TestBooksExportOpensInTheBrowser: JSON, inline — a browser shows it rather than saving it.
func TestBooksExportOpensInTheBrowser(t *testing.T) {
	a := testApp()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/books.json", nil)
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	a.serveBooksExport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Fatalf("Content-Disposition = %q — anything but inline downloads", cd)
	}
	var got booksExport
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
}
