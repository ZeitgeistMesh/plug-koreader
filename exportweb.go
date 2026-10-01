package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// booksExport is /books.json: the reading history as data, one entry per book.
//
// METADATA AND COUNTS, NEVER CONTENT. A highlight's text, a note, a captured page and a
// chapter name are what the reader wrote or read, and this file is the kind of thing that
// gets pasted into another tool. So annotations appear only as counts, and the export is
// built from fields that cannot carry text — there is no struct here a quote could ride.
type booksExport struct {
	GeneratedAt string         `json:"generated_at"`
	Zone        string         `json:"zone"`
	Devices     []exportDevice `json:"devices"`
	Totals      exportTotals   `json:"totals"`
	Books       []exportBook   `json:"books"`
}

type exportDevice struct {
	Name            string `json:"name"`
	Model           string `json:"model,omitempty"`
	KOReaderVersion string `json:"koreader_version,omitempty"`
	PairedAt        string `json:"paired_at,omitempty"`
	LastSyncAt      string `json:"last_sync_at,omitempty"`
	Revoked         bool   `json:"revoked"`
}

type exportTotals struct {
	Books      int `json:"books"`
	Seconds    int `json:"seconds"`
	DaysRead   int `json:"days_read"`
	PageTurns  int `json:"page_turns"`
	Highlights int `json:"highlights"`
	Bookmarks  int `json:"bookmarks"`
	Notes      int `json:"notes"`
}

type exportBook struct {
	MD5          string `json:"md5"`
	Title        string `json:"title"`
	DisplayTitle string `json:"display_title"`
	Authors      string `json:"authors,omitempty"`
	// BookPages is the book's length as the device last reported it; 0 when the book has
	// annotations but no reading rows.
	BookPages int `json:"book_pages"`
	Seconds   int `json:"seconds"`
	// PagesRead sums each day's distinct pages, which is the /books "Pages" column;
	// DistinctPages counts a page once however many days it was read on.
	PagesRead     int    `json:"pages_read"`
	DistinctPages int    `json:"distinct_pages"`
	PageTurns     int    `json:"page_turns"`
	DaysRead      int    `json:"days_read"`
	FirstRead     string `json:"first_read,omitempty"`
	LastRead      string `json:"last_read,omitempty"`
	Highlights    int    `json:"highlights"`
	Bookmarks     int    `json:"bookmarks"`
	// BookmarksWithPage counts bookmarks whose page has been captured.
	BookmarksWithPage int `json:"bookmarks_with_page"`
	Notes             int `json:"notes"`
	// Quotes is the /books "Quotes" column: everything carrying text, which includes a
	// bookmark once its page is captured.
	Quotes        int         `json:"quotes"`
	LastAnnotated string      `json:"last_annotated,omitempty"`
	Days          []exportDay `json:"days"`
}

type exportDay struct {
	Date    string `json:"date"`
	Seconds int    `json:"seconds"`
	Pages   int    `json:"pages"`
}

// buildBooksExport derives the export from the same index /books and the calendar use, so
// the three can never disagree about the same reading.
func buildBooksExport(idx *readingIndex, devices []Device, loc *time.Location, now time.Time) booksExport {
	stamp := func(unix int64) string {
		if unix <= 0 {
			return ""
		}
		return time.Unix(unix, 0).In(loc).Format(time.RFC3339)
	}
	byMD5 := map[string]*exportBook{}
	book := func(md5, title, authors string) *exportBook {
		b := byMD5[md5]
		if b == nil {
			b = &exportBook{MD5: md5, Days: []exportDay{}}
			byMD5[md5] = b
		}
		if b.Title == "" && strings.TrimSpace(title) != "" {
			b.Title, b.Authors = title, authors
		}
		return b
	}

	for _, date := range idx.Dates {
		for _, bd := range idx.Days[date].Books {
			b := book(bd.BookMD5, bd.Title, bd.Authors)
			b.Seconds += bd.Seconds
			b.PagesRead += bd.Pages
			b.Days = append(b.Days, exportDay{Date: date, Seconds: bd.Seconds, Pages: bd.Pages})
		}
	}

	first, last := map[string]int64{}, map[string]int64{}
	distinct := map[string]map[int]bool{}
	for _, ev := range idx.Reads {
		b := book(ev.BookMD5, ev.Title, ev.Authors)
		b.PageTurns++
		if ev.Pages > 0 {
			b.BookPages = ev.Pages // Reads is oldest first, so this ends on the latest
		}
		if distinct[ev.BookMD5] == nil {
			distinct[ev.BookMD5] = map[int]bool{}
		}
		distinct[ev.BookMD5][ev.Page] = true
		if f, ok := first[ev.BookMD5]; !ok || ev.StartTime < f {
			first[ev.BookMD5] = ev.StartTime
		}
		if end := ev.StartTime + int64(ev.Duration); end > last[ev.BookMD5] {
			last[ev.BookMD5] = end
		}
	}

	lastAnn := map[string]int64{}
	for _, an := range idx.Annotations {
		b := book(an.BookMD5, an.Title, an.Authors)
		hasText := strings.TrimSpace(an.Text) != ""
		switch an.Kind {
		case KindHighlight:
			b.Highlights++
		case KindBookmark:
			b.Bookmarks++
			if hasText {
				b.BookmarksWithPage++
			}
		}
		if hasText {
			b.Quotes++
		}
		if strings.TrimSpace(an.Note) != "" {
			b.Notes++
		}
		if an.StartTime > lastAnn[an.BookMD5] {
			lastAnn[an.BookMD5] = an.StartTime
		}
	}

	out := booksExport{
		GeneratedAt: now.In(loc).Format(time.RFC3339),
		Zone:        loc.String(),
		Devices:     []exportDevice{},
		Books:       make([]exportBook, 0, len(byMD5)),
	}
	for _, d := range devices {
		out.Devices = append(out.Devices, exportDevice{
			Name: d.Name, Model: d.DeviceModel, KOReaderVersion: d.KOReaderVersion,
			PairedAt: d.CreatedAt, LastSyncAt: d.LastSyncAt, Revoked: d.Revoked,
		})
	}
	for md5, b := range byMD5 {
		b.DisplayTitle = displayTitle(b.Title)
		b.DaysRead = len(b.Days)
		b.DistinctPages = len(distinct[md5])
		b.FirstRead, b.LastRead = stamp(first[md5]), stamp(last[md5])
		b.LastAnnotated = stamp(lastAnn[md5])
		out.Books = append(out.Books, *b)

		out.Totals.Seconds += b.Seconds
		out.Totals.PageTurns += b.PageTurns
		out.Totals.Highlights += b.Highlights
		out.Totals.Bookmarks += b.Bookmarks
		out.Totals.Notes += b.Notes
	}
	out.Totals.Books = len(out.Books)
	out.Totals.DaysRead = len(idx.Dates)

	// Most recently read first; a book only annotated sorts by its newest annotation.
	// Compared as Unix seconds, not as the formatted strings: across a clock change the
	// local offset differs and the strings stop sorting by time.
	recent := func(b exportBook) int64 {
		if last[b.MD5] > lastAnn[b.MD5] {
			return last[b.MD5]
		}
		return lastAnn[b.MD5]
	}
	sort.SliceStable(out.Books, func(i, j int) bool {
		ri, rj := recent(out.Books[i]), recent(out.Books[j])
		if ri != rj {
			return ri > rj
		}
		return out.Books[i].MD5 < out.Books[j].MD5
	})
	return out
}

// serveBooksExport answers /books.json.
//
// SHOWN IN THE BROWSER, NOT DOWNLOADED. application/json with `inline` disposition, so a
// browser opens it in its JSON viewer and the link can be shared with a tool that fetches
// it; the filename is only what "Save as" suggests. Indented, because the first reader is
// a person.
func (a *app) serveBooksExport(w http.ResponseWriter, r *http.Request) {
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
	devices, _ := a.devices.List(r.Context(), tk)
	loc := a.deviceLoc(r.Context(), tk)

	body, err := json.MarshalIndent(buildBooksExport(idx, devices, loc, time.Now()), "", "  ")
	if err != nil {
		http.Error(w, "could not build the export", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="koreader-books.json"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(append(body, '\n'))
}
