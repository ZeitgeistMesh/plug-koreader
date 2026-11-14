package main

// quoteweb.go — browsing your quotes by book.
//
// The one-quote reader and the searchable list are the shared quote browser now
// (quotebrowse.go). This page is the way in by book: every book you have read, and for
// one with quotes, a link into the reader scoped to it (/quote?book=<md5>), which walks
// that book's quotes in reading order.

import (
	"fmt"
	"strings"
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
