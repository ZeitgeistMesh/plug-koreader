package main

// quoteweb.go — reading your quotes one at a time, and browsing them by book.
//
// A flat searchable list is the right way to FIND a quote and the wrong way to READ one.
// These are passages from books; they deserve the page, not a clipped table cell. So the
// list stays (it is how you search), and it now opens into a reader: one quote, full
// width, with Previous / "N of M" / Next.
//
// The pager is controller/pkg/reviewqueue — the SAME component the plan-review queue and
// org's retrospective use. Worth taking rather than writing: it already handles the
// position arithmetic, the htmx swap, and pushing the URL so browser back/forward work.

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/reviewqueue"
	"xf/controller/pkg/webui"
)

// quotePagerOpts is this plug's reviewqueue.Options.
//
// FullNav, deliberately. The htmx variant swaps a target in place, which is right for a
// queue embedded in a larger page; the reader IS the page, and a full navigation keeps the
// URL, the title and the back button honest with no wiring of its own.
func quotePagerOpts(scope string) reviewqueue.Options {
	extra := ""
	if scope != "" {
		extra = "book=" + url.QueryEscape(scope)
	}
	return reviewqueue.Options{
		BasePath:   "/quote",
		Param:      "id",
		CountLabel: "",
		ExtraQuery: extra,
		FullNav:    true,
		WrapClass:  "ko-pager", PrevClass: "ko-pager-prev",
		CountClass: "ko-pager-count", NextClass: "ko-pager-next",
	}
}

// scopedHighlights returns the highlights the reader is paging through, and the book's
// title when the view is scoped to one.
//
// The ORDER IS THE READING ORDER, oldest first, so paging forward walks a book the way you
// read it. The flat list is newest-first because that is what a search wants; these are
// different questions and they get different answers rather than one compromise.
func scopedHighlights(idx *readingIndex, bookMD5 string) ([]ReadEvent, string) {
	all := idx.Highlights()
	out := make([]ReadEvent, 0, len(all))
	title := ""
	for _, h := range all {
		if bookMD5 != "" && h.BookMD5 != bookMD5 {
			continue
		}
		if bookMD5 != "" && title == "" {
			title = h.Title
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BookMD5 != out[j].BookMD5 {
			return out[i].BookMD5 < out[j].BookMD5
		}
		if out[i].Page != out[j].Page {
			return out[i].Page < out[j].Page
		}
		return out[i].StartTime < out[j].StartTime
	})
	return out, title
}

// serveQuote is the one-quote reader.
func (a *app) serveQuote(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	loc := a.deviceLoc(r.Context(), tk)
	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return
	}

	book := strings.TrimSpace(r.URL.Query().Get("book"))
	quotes, bookTitle := scopedHighlights(idx, book)
	if len(quotes) == 0 {
		p := a.page("/quotes")
		p.StartFor(w, r, "Quote")
		fmt.Fprint(w, `<div class="card"><p class="muted">No quotes here. <a href="/quotes">All quotes</a></p></div>`)
		p.End(w)
		return
	}

	ids := make([]string, 0, len(quotes))
	for _, q := range quotes {
		ids = append(ids, q.ID)
	}
	current := strings.TrimSpace(r.URL.Query().Get("id"))
	if current == "" {
		current = ids[0]
	}
	var shown ReadEvent
	found := false
	for _, q := range quotes {
		if q.ID == current {
			shown, found = q, true
			break
		}
	}
	if !found {
		shown, current = quotes[0], ids[0]
	}
	queue := reviewqueue.Locate(ids, current)

	p := a.page("/quotes")
	p.StartFor(w, r, "Quote")

	fmt.Fprint(w, `<div class="field-row btn-row">`)
	if book != "" {
		fmt.Fprint(w, string(webui.IconTextButton("fa-solid fa-arrow-left", "All books",
			webui.BtnOpts{Kind: "secondary", Size: "sm", Href: "/books"})))
	}
	fmt.Fprint(w, string(webui.IconTextButton("fa-solid fa-list", "Every quote",
		webui.BtnOpts{Kind: "secondary", Size: "sm", Href: "/quotes"})))
	fmt.Fprint(w, `</div>`)

	if bookTitle != "" {
		fmt.Fprintf(w, `<p class="muted">%s</p>`, webui.Text(bookTitle))
	}

	fmt.Fprint(w, `<div class="card ko-reader">`)
	fmt.Fprintf(w, `<blockquote class="ko-quote ko-highlight"><p class="ko-quote-text ko-quote-big">%s</p>`,
		webui.Text(shown.Text))
	if strings.TrimSpace(shown.Note) != "" && strings.TrimSpace(shown.Note) != strings.TrimSpace(shown.Text) {
		fmt.Fprintf(w, `<p class="ko-quote-note">%s</p>`, webui.Text(shown.Note))
	}
	fmt.Fprint(w, `</blockquote>`)

	// WHEN IT WAS ANNOTATED, on the same screen, because that is half of what makes a
	// quote mean something later: a passage marked during a hard week reads differently
	// from the same words marked on a train.
	fmt.Fprint(w, `<dl class="ko-meta">`)
	metaRow(w, "Book", orUntitled(shown.Title))
	if strings.TrimSpace(shown.Authors) != "" {
		metaRow(w, "Author", shown.Authors)
	}
	if strings.TrimSpace(shown.Chapter) != "" {
		metaRow(w, "Chapter", shown.Chapter)
	}
	if shown.Page > 0 {
		metaRow(w, "Page", fmt.Sprintf("%d", shown.Page))
	}
	if shown.StartTime > 0 {
		when := time.Unix(shown.StartTime, 0).In(loc)
		metaRowHTML(w, "Annotated", template.HTML(
			fmt.Sprintf(`<a href="/day?d=%s">%s</a>`,
				readingDate(shown.StartTime, loc), webui.Text(when.Format("Monday 2 January 2006, 15:04")))))
	}
	fmt.Fprint(w, `</dl>`)
	fmt.Fprint(w, string(reviewqueue.PagerHTML(queue, quotePagerOpts(book))))
	fmt.Fprint(w, `</div>`)
	p.End(w)
}

func metaRow(w http.ResponseWriter, label, value string) {
	metaRowHTML(w, label, webui.Text(value))
}

func metaRowHTML(w http.ResponseWriter, label string, value template.HTML) {
	fmt.Fprintf(w, `<dt>%s</dt><dd>%s</dd>`, webui.Text(label), value)
}

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
		title := webui.Text(orUntitled(b.Title))
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
