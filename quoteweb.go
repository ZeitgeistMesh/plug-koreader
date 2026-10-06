package main

// quoteweb.go — browsing your quotes by book.
//
// The one-quote reader and the searchable list are the shared quote browser now
// (quotebrowse.go). This page is the way in by book: every book you have read, and for
// one with quotes, a link into the reader scoped to it (/quote?book=<md5>), which walks
// that book's quotes in reading order.

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// BookShelf is one book, summarised over everything that has synced for it.
type BookShelf struct {
	MD5        string
	Title      string
	Authors    string
	Highlights int
	Bookmarks  int
	Seconds    int
	Pages      int
	Days       int
	LastRead   int64
}

// shelf aggregates events per book. Built from the same index the calendar uses, so a book
// on this page and a day on that one can never disagree about the same reading.
func shelf(idx *readingIndex) []BookShelf {
	byMD5 := map[string]*BookShelf{}
	days := map[string]map[string]bool{}

	for _, day := range idx.Days {
		for _, bd := range day.Books {
			b := byMD5[bd.BookMD5]
			if b == nil {
				b = &BookShelf{MD5: bd.BookMD5, Title: bd.Title, Authors: bd.Authors}
				byMD5[bd.BookMD5] = b
				days[bd.BookMD5] = map[string]bool{}
			}
			if b.Title == "" {
				b.Title, b.Authors = bd.Title, bd.Authors
			}
			b.Seconds += bd.Seconds
			b.Pages += bd.Pages
			days[bd.BookMD5][day.Date] = true
		}
	}
	for _, an := range idx.Annotations {
		b := byMD5[an.BookMD5]
		if b == nil {
			// A book can have highlights and no reading rows — the statistics database
			// and the sidecars are different files, and one can be pruned without the
			// other. Dropping it here would lose quotes that exist.
			b = &BookShelf{MD5: an.BookMD5, Title: an.Title, Authors: an.Authors}
			byMD5[an.BookMD5] = b
			days[an.BookMD5] = map[string]bool{}
		}
		if b.Title == "" {
			b.Title, b.Authors = an.Title, an.Authors
		}
		// Counted by whether it carries text, matching readingIndex.Highlights: a
		// bookmark whose page has been captured is a quote, and a bookmark still
		// waiting for its page is not yet one.
		if strings.TrimSpace(an.Text) != "" {
			b.Highlights++
		} else if an.Kind == KindBookmark {
			b.Bookmarks++
		}
		if an.StartTime > b.LastRead {
			b.LastRead = an.StartTime
		}
	}
	out := make([]BookShelf, 0, len(byMD5))
	for md5, b := range byMD5 {
		b.Days = len(days[md5])
		out = append(out, *b)
	}
	return out
}

// serveBooks is the by-book browser.
func (a *app) serveBooks(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return
	}

	view := booksListView()
	q := view.ParseListQuery(r)
	rows, total := bookRows(shelf(idx), q)

	if flowweb.WantsFragment(r) && r.Method == http.MethodGet {
		view.RenderFragment(w, q, rows, total)
		return
	}
	p := a.page("/books")
	p.StartFor(w, r, "Books")
	view.Render(w, q, rows, total)
	p.End(w)
}

func booksListView() webui.ListView {
	return webui.ListView{
		Action:            "/books",
		RootID:            "lv-books",
		Title:             "Books",
		SearchPlaceholder: "Search your books…",
		EmptyText:         "No book matches that.",
		PageSizes:         []int{25, 50, 100},
		Columns: []webui.Column{
			{Key: "title", Label: "Book", Sortable: true},
			{Key: "quotes", Label: "Quotes", Sortable: true, Numeric: true},
			{Key: "time", Label: "Time", Sortable: true, Numeric: true, Priority: 1},
			{Key: "pages", Label: "Pages", Sortable: true, Numeric: true, Priority: 2},
			{Key: "days", Label: "Days", Sortable: true, Numeric: true, Priority: 3},
		},
	}
}

func bookRows(books []BookShelf, q webui.ListQuery) ([]webui.Row, int) {
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	filtered := make([]BookShelf, 0, len(books))
	for _, b := range books {
		if needle != "" && !strings.Contains(strings.ToLower(b.Title+" "+b.Authors), needle) {
			continue
		}
		filtered = append(filtered, b)
	}

	// Default: most-highlighted first. The page exists to reach quotes, so the book you
	// have marked up most is the one most likely to be wanted.
	less := func(i, j int) bool {
		if filtered[i].Highlights != filtered[j].Highlights {
			return filtered[i].Highlights > filtered[j].Highlights
		}
		return filtered[i].Seconds > filtered[j].Seconds
	}
	switch q.Sort {
	case "title":
		less = func(i, j int) bool {
			a, b := strings.ToLower(filtered[i].Title), strings.ToLower(filtered[j].Title)
			if q.Desc {
				return a > b
			}
			return a < b
		}
	case "quotes":
		less = func(i, j int) bool { return cmpInt(filtered[i].Highlights, filtered[j].Highlights, q.Desc) }
	case "time":
		less = func(i, j int) bool { return cmpInt(filtered[i].Seconds, filtered[j].Seconds, q.Desc) }
	case "pages":
		less = func(i, j int) bool { return cmpInt(filtered[i].Pages, filtered[j].Pages, q.Desc) }
	case "days":
		less = func(i, j int) bool { return cmpInt(filtered[i].Days, filtered[j].Days, q.Desc) }
	}
	sort.SliceStable(filtered, less)

	total := len(filtered)
	lo := q.Offset()
	if lo > total {
		lo = total
	}
	hi := lo + q.Limit()
	if hi > total {
		hi = total
	}

	rows := make([]webui.Row, 0, hi-lo)
	for _, b := range filtered[lo:hi] {
		title := webui.Text(displayTitle(b.Title))
		// Only a book with quotes links into the reader — a link that opens an empty
		// reader is worse than no link.
		if b.Highlights > 0 {
			title = template.HTML(fmt.Sprintf(`<a href="/quote?book=%s">%s</a>`,
				url.QueryEscape(b.MD5), title))
		}
		quotes := webui.Text("—")
		if b.Highlights > 0 {
			quotes = webui.Text(fmt.Sprintf("%d", b.Highlights))
		}
		rows = append(rows, webui.Row{
			"title":  title,
			"quotes": quotes,
			"time":   webui.Text(humanDuration(b.Seconds)),
			"pages":  webui.Text(fmt.Sprintf("%d", b.Pages)),
			"days":   webui.Text(fmt.Sprintf("%d", b.Days)),
		})
	}
	return rows, total
}

func cmpInt(a, b int, desc bool) bool {
	if desc {
		return a > b
	}
	return a < b
}

// humanDuration prefers hours once there are enough of them to matter — "9h" reads, "543
// min" does not.
func humanDuration(seconds int) string {
	switch {
	case seconds <= 0:
		return "—"
	case seconds < 3600:
		return fmt.Sprintf("%d min", maxInt(seconds/60, 1))
	default:
		return fmt.Sprintf("%.1f h", float64(seconds)/3600)
	}
}
