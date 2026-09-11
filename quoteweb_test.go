package main

import (
	"strings"
	"testing"
	"time"

	"xf/controller/pkg/reviewqueue"
	"xf/controller/pkg/webui"
)

func quoteFixture() *readingIndex {
	const base = int64(1757000000)
	return buildReadingIndex([]ReadEvent{
		// Two books, deliberately out of both page and time order on input.
		{ID: "b-p9", Kind: KindHighlight, BookMD5: "bbb", Title: "Second", Page: 9, StartTime: base + 50, Text: "nine"},
		{ID: "a-p2", Kind: KindHighlight, BookMD5: "aaa", Title: "First", Page: 2, StartTime: base + 900, Text: "two"},
		{ID: "a-p1", Kind: KindHighlight, BookMD5: "aaa", Title: "First", Page: 1, StartTime: base + 100, Text: "one"},
		{ID: "b-p3", Kind: KindHighlight, BookMD5: "bbb", Title: "Second", Page: 3, StartTime: base + 10, Text: "three"},
		// A bookmark has no quote and must never enter the reader.
		{ID: "mark", Kind: KindBookmark, BookMD5: "aaa", Title: "First", Page: 4, StartTime: base},
		{ID: "read", Kind: KindRead, BookMD5: "aaa", Title: "First", Page: 1, StartTime: base, Duration: 300},
	}, time.UTC)
}

// TestReaderPagesInReadingOrder: paging forward should walk a book the way it was read.
// The flat list is newest-first because that is what searching wants; these are different
// questions and conflating them gives a reader that jumps around mid-book.
func TestReaderPagesInReadingOrder(t *testing.T) {
	quotes, title := scopedHighlights(quoteFixture(), "aaa")
	if title != "First" {
		t.Errorf("book title = %q, want First", title)
	}
	var got []string
	for _, q := range quotes {
		got = append(got, q.ID)
	}
	if strings.Join(got, ",") != "a-p1,a-p2" {
		t.Errorf("scoped order = %v, want [a-p1 a-p2] — by page, ascending", got)
	}
}

// TestReaderScopeExcludesOtherBooksAndBookmarks.
func TestReaderScopeExcludesOtherBooksAndBookmarks(t *testing.T) {
	idx := quoteFixture()

	scoped, _ := scopedHighlights(idx, "aaa")
	for _, q := range scoped {
		if q.BookMD5 != "aaa" {
			t.Errorf("book scope leaked %q", q.ID)
		}
		if q.Kind != KindHighlight {
			t.Errorf("a %s entered the reader", q.Kind)
		}
	}

	all, _ := scopedHighlights(idx, "")
	if len(all) != 4 {
		t.Fatalf("unscoped reader has %d quotes, want 4 (bookmarks and read rows excluded)", len(all))
	}
	for _, q := range all {
		if q.ID == "mark" || q.ID == "read" {
			t.Errorf("%q must not be a quote", q.ID)
		}
	}
}

// TestPagerLocatesWithinTheScope pins the Prev/Next wiring: the first quote has no
// previous, the last has no next, and the count is the scope's size — not the library's.
func TestPagerLocatesWithinTheScope(t *testing.T) {
	quotes, _ := scopedHighlights(quoteFixture(), "aaa")
	ids := make([]string, 0, len(quotes))
	for _, q := range quotes {
		ids = append(ids, q.ID)
	}

	first := reviewqueue.Locate(ids, "a-p1")
	if first.Position != 1 || first.Total != 2 || first.PrevID != "" || first.NextID != "a-p2" {
		t.Errorf("first quote: %+v", first)
	}
	last := reviewqueue.Locate(ids, "a-p2")
	if last.Position != 2 || last.NextID != "" || last.PrevID != "a-p1" {
		t.Errorf("last quote: %+v", last)
	}
}

// TestPagerCarriesTheBookScope: the scope must survive Prev/Next, or the second page of a
// book's quotes silently becomes the whole library.
func TestPagerCarriesTheBookScope(t *testing.T) {
	html := string(reviewqueue.PagerHTML(
		reviewqueue.Queue{Position: 1, Total: 2, NextID: "a-p2"}, quotePagerOpts("aaa")))
	if !strings.Contains(html, "book=aaa") {
		t.Errorf("the pager link drops the book scope: %s", html)
	}
	if !strings.Contains(html, "/quote?id=a-p2") {
		t.Errorf("the pager link is not pointing at the reader: %s", html)
	}
	// FullNav: a real navigation, so no htmx swap attributes.
	if strings.Contains(html, "hx-get") {
		t.Errorf("the reader pager should be a full navigation, got an htmx swap: %s", html)
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
