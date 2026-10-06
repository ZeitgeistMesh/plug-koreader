package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
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

// P0 is browse-only: koreader draws no star or tag control until vision exists to hold them.
func TestTheBrowserIsBrowseOnly(t *testing.T) {
	a := testApp()
	quoteFixture(t, a)
	for _, target := range []string{"/quotes", "/quote"} {
		if body := browse(t, a, target); strings.Contains(body, "data-qb-control") {
			t.Errorf("%s draws a control", target)
		}
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
