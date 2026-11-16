package main

// library.go — /books, the one list of books. What the book server holds and what the device
// has read are the same books, so they are one list with one design; "Read" is a filter on it,
// not a second page (owner, plan 26856ccd: two similar screens that differ slightly make a
// person wonder why there are two).
//
// A row is a book. It carries the cover and the Read/download buttons when the connected server
// holds it, and the quotes, time, pages and days when the device has read it. The two sides are
// joined by title, the way a quoted book has always found its EPUB (bookReadLink).

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// Copy, written by Opus.
const (
	libShowParam = "show"
	libShowAll   = "all"
	libShowRead  = "read"
	libTabAll    = "All books"
	libTabRead   = "Read"
	libEmptyRead = "Nothing read yet. Sync a reader and the books you have read appear here."
	libSwitchAria = "Which books"
	libUnnamed    = "Unnamed book"
	libUnnamedTip = "Your reader sent this reading without a title, so it cannot be matched to a book on your server."
)

// libBook is one row: the catalog entry (nil when the server does not hold it) and what was read
// of it (nil when nothing has been).
type libBook struct {
	Entry *CatalogEntry
	Shelf *BookShelf
}

// unescapeOnce undoes the layers of HTML entities. A book server's feed can carry a title that was escaped
// twice ("&#40;Kate Daniels 1&#41;"), which the page then escaped a third time and printed as code
// (owner via master, plan 26856ccd); the text is made plain once here and escaped once when drawn.
//
// UNTIL IT STOPS CHANGING (at most three passes): the feed this came from had escaped the text twice before it
// reached us, so one pass left "&#40;" behind. Plain text with no entities is returned untouched.
func unescapeOnce(s string) string {
	for i := 0; i < 3; i++ {
		u := html.UnescapeString(s)
		if u == s {
			break
		}
		s = u
	}
	return s
}

func (b libBook) title() string {
	if b.Entry != nil {
		return unescapeOnce(b.Entry.Title)
	}
	return b.Shelf.Title
}

func (b libBook) authors() string {
	if b.Entry != nil && strings.TrimSpace(b.Entry.Authors) != "" {
		return unescapeOnce(b.Entry.Authors)
	}
	if b.Shelf != nil {
		return b.Shelf.Authors
	}
	return ""
}

// buildLibrary joins the two lists by title. Several editions of one title share the stats, which
// attach to the first of them so the numbers are not counted twice.
func buildLibrary(entries []CatalogEntry, shelves []BookShelf) []libBook {
	byTitle := map[string]int{} // normalised title -> index in shelves
	for i, s := range shelves {
		if k := normTitle(s.Title); k != "" {
			if _, dup := byTitle[k]; !dup {
				byTitle[k] = i
			}
		}
	}
	used := map[int]bool{}
	out := make([]libBook, 0, len(entries)+len(shelves))
	for i := range entries {
		b := libBook{Entry: &entries[i]}
		if si, ok := byTitle[normTitle(displayTitle(unescapeOnce(entries[i].Title)))]; ok && !used[si] {
			used[si] = true
			s := shelves[si]
			b.Shelf = &s
		}
		out = append(out, b)
	}
	for i := range shelves {
		if !used[i] {
			s := shelves[i]
			out = append(out, libBook{Shelf: &s})
		}
	}
	return out
}

func libraryListView() webui.ListView {
	return webui.ListView{
		Action:            booksPath,
		RootID:            "lv-books",
		SearchPlaceholder: ui.SearchPlaceholder,
		EmptyText:         ui.EmptyNoneMatch,
		PageSizes:         []int{25, 50, 100},
		Preserve:          []string{libShowParam},
		Columns: []webui.Column{
			{Key: "cover", Label: ""},
			{Key: "title", Label: ui.ColTitle, Sortable: true},
			{Key: "authors", Label: ui.ColAuthor, Sortable: true, Priority: 1},
			{Key: "quotes", Label: "Quotes", Sortable: true, Numeric: true, Priority: 2},
			{Key: "time", Label: "Time", Sortable: true, Numeric: true, Priority: 2},
			{Key: "pages", Label: "Pages", Sortable: true, Numeric: true, Priority: 3},
			{Key: "days", Label: "Days", Sortable: true, Numeric: true, Priority: 3},
			{Key: "formats", Label: ""},
		},
	}
}

// libraryRows filters, sorts and pages the joined list. read keeps only books that have been read.
func libraryRows(books []libBook, q webui.ListQuery, read bool) ([]webui.Row, int) {
	needle := strings.Fields(strings.ToLower(q.Search))
	kept := make([]libBook, 0, len(books))
	for _, b := range books {
		if read && b.Shelf == nil {
			continue
		}
		hay := strings.ToLower(b.title() + " " + b.authors())
		ok := true
		for _, w := range needle {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, b)
		}
	}
	stat := func(b libBook, f func(*BookShelf) int) int {
		if b.Shelf == nil {
			return 0
		}
		return f(b.Shelf)
	}
	quotes := func(s *BookShelf) int { return s.Highlights }
	secs := func(s *BookShelf) int { return s.Seconds }
	pages := func(s *BookShelf) int { return s.Pages }
	days := func(s *BookShelf) int { return s.Days }
	// The default order: A to Z for the whole list; most-marked-up first for what you have read, since
	// that is the book most likely to be wanted.
	less := func(i, j int) bool { return cmpText(kept[i].title(), kept[j].title(), false) }
	if read {
		less = func(i, j int) bool {
			if a, b := stat(kept[i], quotes), stat(kept[j], quotes); a != b {
				return a > b
			}
			return stat(kept[i], secs) > stat(kept[j], secs)
		}
	}
	switch q.Sort {
	case "title":
		less = func(i, j int) bool { return cmpText(kept[i].title(), kept[j].title(), q.Desc) }
	case "authors":
		less = func(i, j int) bool { return cmpText(kept[i].authors(), kept[j].authors(), q.Desc) }
	case "quotes":
		less = func(i, j int) bool { return cmpInt(stat(kept[i], quotes), stat(kept[j], quotes), q.Desc) }
	case "time":
		less = func(i, j int) bool { return cmpInt(stat(kept[i], secs), stat(kept[j], secs), q.Desc) }
	case "pages":
		less = func(i, j int) bool { return cmpInt(stat(kept[i], pages), stat(kept[j], pages), q.Desc) }
	case "days":
		less = func(i, j int) bool { return cmpInt(stat(kept[i], days), stat(kept[j], days), q.Desc) }
	}
	sort.SliceStable(kept, less)

	total := len(kept)
	lo := min(q.Offset(), total)
	hi := min(lo+q.Limit(), total)
	rows := make([]webui.Row, 0, hi-lo)
	for _, b := range kept[lo:hi] {
		rows = append(rows, libraryRow(b))
	}
	return rows, total
}

func btnHTML(in []string) []template.HTML {
	out := make([]template.HTML, len(in))
	for i, h := range in {
		out[i] = template.HTML(h)
	}
	return out
}

func libraryRow(b libBook) webui.Row {
	cover := webui.Text("")
	full := displayTitle(b.title())
	// Two lines at most, so a long title does not make its row tall; the whole title is the tooltip.
	// The whole title, wrapped: no clamp and no native tooltip repeating it (pagelint /books: five
	// titles cut at two lines; the owner never asked for even rows, only aligned buttons).
	title := template.HTML(`<span class="rd-title">` + html.EscapeString(full) + `</span>`)
	if strings.TrimSpace(b.title()) == "" {
		// A reading the device sent with no title cannot be matched to a book on the server. Say so,
		// rather than a bare "Untitled" beside an author nobody recognises.
		title = template.HTML(`<span class="muted" data-tip="` + html.EscapeString(libUnnamedTip) + `">` + html.EscapeString(libUnnamed) + `</span>`)
	}
	var buttons []string
	if e := b.Entry; e != nil {
		key := entryKey(e.ID)
		if e.Cover != "" {
			cover = template.HTML(fmt.Sprintf(`<img class="rd-cover" loading="lazy" alt="" src="%s/cover?id=%s">`, booksPath, key))
		}
		// Read and each download are the same house button, in one row with the house gap
		// (owner, plan 26856ccd: the EPUB link sat tight against Read, in another theme).
		if readableFile(e) {
			buttons = append(buttons, string(webui.IconTextButton("fa-solid fa-book-open-reader", ui.ReadLink,
				webui.BtnOpts{Kind: "secondary", Size: "sm", Href: booksPath + "/read?id=" + key, Attrs: ` hx-boost="false" data-tip="` + html.EscapeString(ui.ReadTip) + `"`})))
		}
		for i, f := range e.Files {
			buttons = append(buttons, string(webui.IconTextButton("fa-solid fa-download", strings.ToUpper(fileExt(f.Type)),
				webui.BtnOpts{Kind: "secondary", Size: "sm", Href: fmt.Sprintf("%s/file?id=%s&f=%d", booksPath, key, i), Attrs: ` hx-boost="false" data-tip="` + html.EscapeString(ui.DownloadTitle) + `"`})))
		}
	}
	dash := webui.Text("—")
	acts := webui.Text("")
	if len(buttons) > 0 {
		acts = template.HTML(`<div class="rd-acts">` + string(webui.ButtonGroup(btnHTML(buttons)...)) + `</div>`)
	}
	row := webui.Row{
		"cover": cover, "title": title, "authors": webui.Text(b.authors()),
		"quotes": dash, "time": dash, "pages": dash, "days": dash,
		"formats": acts,
	}
	if s := b.Shelf; s != nil {
		// The title is plain text in every row, whichever filter shows it. The way into a book's quotes is its
		// Quotes count, and only a book with quotes has one: a link that opens an empty reader is worse than none.
		if s.Highlights > 0 {
			row["quotes"] = template.HTML(fmt.Sprintf(`<a href="/quote?book=%s">%d</a>`, url.QueryEscape(s.MD5), s.Highlights))
		}
		row["time"] = webui.Text(humanDuration(s.Seconds))
		row["pages"] = webui.Text(fmt.Sprintf("%d", s.Pages))
		row["days"] = webui.Text(fmt.Sprintf("%d", s.Days))
	}
	return row
}

// libraryShow reads the filter; anything but "read" is the whole list.
func libraryShow(r *http.Request) string {
	if r.URL.Query().Get(libShowParam) == libShowRead {
		return libShowRead
	}
	return libShowAll
}

// librarySwitch is the house two-way switch above the list. It swaps the list in place and keeps
// the address honest, so a reload and Back land on the filter that was showing.
func librarySwitch(active string) template.HTML {
	return webui.ViewToggleOpts("books-show", []webui.ViewTab{
		{View: libShowAll, Label: libTabAll, Icon: "fa-solid fa-book"},
		{View: libShowRead, Label: libTabRead, Icon: "fa-solid fa-book-open-reader"},
	}, active, webui.ViewToggleOptions{
		Base: booksPath, Param: libShowParam, HXTarget: "#lv-books", HXSelect: "#lv-books", HXPushURL: true, Label: libSwitchAria,
	})
}

func (a *app) serveLibrary(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	// A stale or missing list starts a refresh behind the page. The page itself reads only what is saved.
	if catalogDue(c, s, time.Now()) {
		a.refreshSoon(tk)
	}
	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return
	}
	var entries []CatalogEntry
	if c != nil {
		entries = c.Entries
	}
	show := libraryShow(r)
	view := libraryListView()
	view.Header = librarySwitch(show)
	q := view.ParseListQuery(r)
	rows, total := libraryRows(buildLibrary(entries, shelf(idx)), q, show == libShowRead)
	if flowweb.WantsFragment(r) && r.Method == http.MethodGet {
		view.RenderFragment(w, q, rows, total)
		return
	}
	p := a.page(booksPath)
	p.CSS += readerBooksCSS + webui.ViewToggleCSS
	p.StartFor(w, r, ui.BooksTitle)
	if s == nil && show == libShowAll && len(shelf(idx)) == 0 {
		fmt.Fprintf(w, `<p class="hint rd-status">%s <a href="%s">%s</a>.</p>`, html.EscapeString(ui.EmptyNotConnected), settingsPath, html.EscapeString(ui.NavSettings))
		p.End(w)
		return
	}
	if s != nil {
		fmt.Fprint(w, catalogStatus(c, a.isRefreshing(tk)))
	}
	view.Render(w, q, rows, total)
	p.End(w)
}

// serveReadingMoved keeps the old /reading address working: what you have read is now a filter on Books.
func serveReadingMoved(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, booksPath+"?"+libShowParam+"="+libShowRead, http.StatusMovedPermanently)
}
