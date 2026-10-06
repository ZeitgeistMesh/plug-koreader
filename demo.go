package main

// demo.go — the anonymous demo tier.
//
// The honesty test passes: everything this plug shows is computed from the visitor's own
// workspace, so a seeded workspace runs the REAL calendar, day pages and quote browser and
// reaches nothing outside itself.
//
// The seeds are the persona's (controller/pkg/demo/persona, "Alex") READING, because reading
// is what this plug is for: a month of sessions across three books, so the calendar has a
// shape and a streak, and highlights in each, so the quote browser has something to browse.
// They show the range — a highlight with a note, one without, and a bookmark whose page has
// not been captured yet. The passages are from public-domain translations.

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
)

// ensureDemoSeed lays down the persona's records the first time a demo workspace is
// read. Each collection returns early once it has rows, so a visitor who deletes a seeded
// record does not get it silently resurrected under them.
//
// It reports whether it wrote reading, because the reading index is otherwise invalidated
// only by an ingest and a caller holding a cached index must drop it.
func (a *app) ensureDemoSeed(ctx context.Context, tk dblib.TenantKey) bool {
	if !demo.IsDemo(tk) {
		return false
	}
	nonce, ok := demo.NonceFromWorkspace(tk.Workspace)
	if !ok {
		return false
	}
	// Checked once per workspace per process: every page reads through here, and two
	// whole-collection Lists per page view of a demo is load the demo tier has had to
	// shed before (memory demo-tenants-dominate-idle-load).
	if _, done := a.demoSeeded.LoadOrStore(tk.Workspace, true); done {
		return false
	}
	now := time.Now().UTC()
	books := make([]Book, 0, len(demoBooks))
	for i, b := range demoBooks {
		books = append(books, Book{ID: "demo-" + b.md5, Name: b.title, Subject: tk.Subject, Workspace: tk.Workspace,
			CreatedAt: now.AddDate(0, 0, -40+i*9).Format(time.RFC3339)})
	}
	_, _ = demo.SeedOnce(ctx, a.items, tk, books, func(v Book) string { return v.ID })
	seeded, _ := demo.SeedOnce(ctx, a.events, tk, demoReading(nonce, now), func(v ReadEvent) string { return v.ID })
	if seeded {
		a.index.bump(tk.Subject)
	}
	return seeded
}

type demoBook struct{ md5, title, authors string; pages int }

var demoBooks = []demoBook{
	{"demo-meditations", "Meditations", "Marcus Aurelius", 304},
	{"demo-walden", "Walden", "Henry David Thoreau", 352},
	{"demo-seneca", "Moral Letters to Lucilius", "Seneca", 448},
}

// demoMarks are the persona's highlights: book index, days ago, page, chapter, text, note.
// An empty text is a bookmark still waiting for its page.
var demoMarks = []struct {
	book, daysAgo, page int
	chapter, text, note string
}{
	{0, 27, 41, "Book II", "Begin the morning by saying to thyself, I shall meet with the busy-body, the ungrateful, arrogant, deceitful, envious, unsocial.", "Every Monday."},
	{0, 24, 63, "Book IV", "The universe is transformation: our life is what our thoughts make it.", ""},
	{0, 21, 188, "Book X", "No longer talk at all about the kind of man that a good man ought to be, but be such.", "Stop planning, start."},
	{1, 17, 90, "Where I Lived, and What I Lived For", "I went to the woods because I wished to live deliberately, to front only the essential facts of life, and see if I could not learn what it had to teach, and not, when I came to die, discover that I had not lived.", ""},
	{1, 14, 8, "Economy", "The mass of men lead lives of quiet desperation.", ""},
	{1, 11, 92, "Where I Lived, and What I Lived For", "Our life is frittered away by detail. Simplify, simplify.", "The calendar this week."},
	{2, 7, 1, "Letter I", "Nothing, Lucilius, is ours, except time.", ""},
	{2, 3, 47, "Letter XIII", "There are more things, Lucilius, likely to frighten us than there are to crush us; we suffer more often in imagination than in reality.", "Before the interview."},
	{2, 1, 60, "Letter XVI", "", ""},
}

// demoReading is a month of the persona's reading, and the marks made during it. Which days
// were read varies with the visitor's nonce, so each demo looks lived-in rather than stamped,
// while the books and the passages are always the same person's.
func demoReading(nonce string, now time.Time) []ReadEvent {
	h := fnv.New32a()
	_, _ = h.Write([]byte(nonce))
	seed := h.Sum32()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var out []ReadEvent
	for ago := 30; ago >= 0; ago-- {
		// About four days in five, and never the last two, so the streak is current.
		if ago > 1 && (uint32(ago)*2654435761+seed)%5 == 0 {
			continue
		}
		b := demoBooks[(30-ago)*len(demoBooks)/31]
		start := today.AddDate(0, 0, -ago).Add(time.Duration(20+int((seed>>uint(ago%16))%3)) * time.Hour)
		page := 10 + (30-ago)*9
		for n := 0; n < 3; n++ {
			out = append(out, ReadEvent{
				ID: fmt.Sprintf("demo-r-%d-%d", ago, n), Kind: KindRead,
				BookMD5: b.md5, Title: b.title, Authors: b.authors, Pages: b.pages,
				Page: page + n, StartTime: start.Add(time.Duration(n*12) * time.Minute).Unix(), Duration: 300 + 200*n,
				ReceivedAt: now.Format(time.RFC3339),
			})
		}
	}
	for i, m := range demoMarks {
		b := demoBooks[m.book]
		kind := KindHighlight
		if m.text == "" {
			kind = KindBookmark
		}
		at := today.AddDate(0, 0, -m.daysAgo).Add(20*time.Hour + 30*time.Minute)
		out = append(out, ReadEvent{
			ID: fmt.Sprintf("demo-h-%d", i), Kind: kind,
			BookMD5: b.md5, Title: b.title, Authors: b.authors, Pages: b.pages,
			Page: m.page, Chapter: m.chapter, Text: m.text, Note: m.note, StartTime: at.Unix(),
			ReceivedAt: now.Format(time.RFC3339),
		})
	}
	return out
}
