package main

// summary.go — turning raw events into the numbers the pages show.
//
// # WHY THERE IS A CACHE
//
// Every derived number comes from the raw event rows, which is the right storage decision
// (ingest.go explains it) and an awkward read one: this reader already has ~19,800 of them
// and the store's only whole-collection verb is List. Recomputing a year grid from a full
// scan on every page load is seconds of work for an answer that changes only when a device
// syncs.
//
// So the aggregate is built once and held, and the ONLY thing that invalidates it is an
// ingest. That is a real invariant rather than a TTL guess: nothing else writes events.
//
// # DAYS BELONG TO THE READER, NOT THE SERVER
//
// "Which day did I read on" is a question about the user's wall clock. plug-habits has the
// scar tissue for getting this wrong — a whole file of it — where server-local time put
// every computation an hour out under BST and inverted a recommendation. The zone comes
// from plug-time, is resolved once per request, and every failure path falls back to UTC
// rather than time.Local, so a dev box cannot silently disagree with production.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"xf/controller/pkg/dblib"
)

// A READING DAY IS THE DEVICE'S DAY — the date the reader saw on the e-reader's own
// clock when they turned the page.
//
// This replaces two wrong answers, and the second was mine.
//
// First the server converted every timestamp into the user's zone from plug-time. Then,
// when a late-night session landed on the following morning, a 04:00 cutoff was added to
// push it back. Both were reasonable; the evidence says both were wrong.
//
// MEASURED against the owner's own account of a 65-day run: "continuously since 8 July
// every day until 29 August, when I read nothing; resumed on the 30th". Bucketed by the
// device clock the data agrees exactly — 7 July missing, 8 July to 11 September unbroken
// except for one day, and that day is the 29th. Under Europe/London plus the cutoff, the
// gap moved to the 30th: a single session at 02:27 was dragged back into the 29th, which
// inverted the one fact the reader was surest about.
//
// The reason is that this Kobo has NO TIMEZONE SET. It runs and displays UTC, so the
// reader's sense of "which day did I read" is anchored to a UTC clock, whatever zone they
// are standing in. Converting that to Europe/London does not correct an error, it invents
// one — and the cutoff then papered over the symptom while the cause went on shifting days.
//
// So: the offset comes from the DEVICE, reported on every sync, defaulting to UTC. A
// server-side guess about where the reader lives is exactly the thing that was wrong.
func readingDate(ts int64, loc *time.Location) string {
	return time.Unix(ts, 0).In(loc).Format("2006-01-02")
}

// deviceLoc is the zone the reader's own device is keeping time in.
//
// One offset per account rather than per event: a device's zone changes rarely, and
// carrying it on every row to handle the case where it does would cost more than it
// buys. Unknown (a device that has not synced since this field existed) means UTC, which
// is what an unconfigured e-reader runs anyway.
func (a *app) deviceLoc(ctx context.Context, tk dblib.TenantKey) *time.Location {
	devices, err := a.devices.List(ctx, tk)
	if err != nil {
		return time.UTC
	}
	best, seen := 0, ""
	for _, d := range devices {
		if d.Revoked || d.UTCOffset == 0 {
			continue
		}
		if d.LastSyncAt > seen {
			best, seen = d.UTCOffset, d.LastSyncAt
		}
	}
	if best == 0 {
		return time.UTC
	}
	return time.FixedZone("device", best)
}

// BookDay is one book's reading on one day.
type BookDay struct {
	BookMD5 string
	Title   string
	Authors string
	Seconds int
	Pages   int // distinct pages touched, not page-turn events
}

// DaySummary is everything the calendar and the day view need for one date.
type DaySummary struct {
	Date        string // YYYY-MM-DD in the reader's zone
	Seconds     int
	Pages       int
	Books       []BookDay
	Highlights  int
	Bookmarks   int
	FirstAt     int64
	LastAt      int64
	HourSeconds [24]int
}

// Minutes rounds up: a two-minute session is not "0 minutes read".
func (d DaySummary) Minutes() int {
	if d.Seconds == 0 {
		return 0
	}
	if m := d.Seconds / 60; m > 0 {
		return m
	}
	return 1
}

// readingIndex is the whole aggregate for one tenant.
type readingIndex struct {
	Days        map[string]*DaySummary
	Dates       []string // sorted ascending
	Annotations []ReadEvent
	// Reads is every page-turn row, oldest first, so a question about a window (Past, in
	// pastlist.go) is a binary search over the cached index rather than a fresh full scan.
	Reads     []ReadEvent
	TotalRead int
	BuiltAt   time.Time
	Zone      string
}

type indexCache struct {
	mu   sync.Mutex
	m    map[string]*readingIndex
	gen  map[string]int64
	seen map[string]int64
}

func newIndexCache() *indexCache {
	return &indexCache{m: map[string]*readingIndex{}, gen: map[string]int64{}, seen: map[string]int64{}}
}

// bump marks a tenant's aggregate stale. Called from the ingest path, which is the only
// writer of events.
func (c *indexCache) bump(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen[tenantID]++
}

func (c *indexCache) get(tenantID, zone string) (*readingIndex, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.m[tenantID]
	if !ok || idx.Zone != zone || c.seen[tenantID] != c.gen[tenantID] {
		return nil, false
	}
	return idx, true
}

func (c *indexCache) put(tenantID string, idx *readingIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[tenantID] = idx
	c.seen[tenantID] = c.gen[tenantID]
}

// readingIndexFor returns the tenant's aggregate, building it if a sync has landed since
// it was last built.
func (a *app) readingIndexFor(ctx context.Context, caller string, tk dblib.TenantKey) (*readingIndex, error) {
	loc := a.deviceLoc(ctx, tk)
	if idx, ok := a.index.get(caller, loc.String()); ok {
		return idx, nil
	}
	events, err := a.events.List(ctx, tk)
	if err != nil {
		return nil, err
	}
	idx := buildReadingIndex(events, loc)
	a.index.put(caller, idx)
	return idx, nil
}

// buildReadingIndex is the pure aggregation, so it can be tested without a store.
//
// PAGES ARE COUNTED DISTINCTLY. KOReader writes one row per (page, visit), so re-reading
// a page adds a row; summing rows would report "312 pages" for a chapter read twice. The
// honest number for a day is how many distinct pages of that book were open.
func buildReadingIndex(events []ReadEvent, loc *time.Location) *readingIndex {
	idx := &readingIndex{Days: map[string]*DaySummary{}, BuiltAt: time.Now(), Zone: loc.String()}
	type bookKey struct{ date, md5 string }
	books := map[bookKey]*BookDay{}
	pages := map[bookKey]map[int]bool{}

	for i := range events {
		ev := events[i]
		if ev.StartTime <= 0 {
			continue
		}
		t := time.Unix(ev.StartTime, 0).In(loc)
		date := readingDate(ev.StartTime, loc)
		day := idx.Days[date]
		if day == nil {
			day = &DaySummary{Date: date, FirstAt: ev.StartTime, LastAt: ev.StartTime}
			idx.Days[date] = day
		}
		if ev.StartTime < day.FirstAt {
			day.FirstAt = ev.StartTime
		}
		if ev.StartTime > day.LastAt {
			day.LastAt = ev.StartTime
		}

		switch ev.Kind {
		case KindHighlight:
			day.Highlights++
			idx.Annotations = append(idx.Annotations, ev)
			continue
		case KindBookmark:
			day.Bookmarks++
			idx.Annotations = append(idx.Annotations, ev)
			continue
		}

		idx.TotalRead++
		idx.Reads = append(idx.Reads, ev)
		day.Seconds += ev.Duration
		day.HourSeconds[t.Hour()] += ev.Duration

		k := bookKey{date, ev.BookMD5}
		bd := books[k]
		if bd == nil {
			bd = &BookDay{BookMD5: ev.BookMD5, Title: ev.Title, Authors: ev.Authors}
			books[k] = bd
			pages[k] = map[int]bool{}
		}
		bd.Seconds += ev.Duration
		if !pages[k][ev.Page] {
			pages[k][ev.Page] = true
			bd.Pages++
			day.Pages++
		}
	}

	for k, bd := range books {
		day := idx.Days[k.date]
		day.Books = append(day.Books, *bd)
	}
	for _, day := range idx.Days {
		sort.Slice(day.Books, func(i, j int) bool { return day.Books[i].Seconds > day.Books[j].Seconds })
		idx.Dates = append(idx.Dates, day.Date)
	}
	sort.Strings(idx.Dates)

	sort.SliceStable(idx.Reads, func(i, j int) bool { return idx.Reads[i].StartTime < idx.Reads[j].StartTime })

	// Newest annotation first — the review list's natural order.
	sort.Slice(idx.Annotations, func(i, j int) bool {
		return idx.Annotations[i].StartTime > idx.Annotations[j].StartTime
	})
	return idx
}

// Highlights returns every annotation that carries text worth reading back.
//
// THE TEST IS TEXT, NOT KIND. A bookmark used to be a place-marker with nothing to put on
// a card; now the device captures the whole bookmarked page and sends it as that
// annotation's text, so a bookmark becomes a quote — a longer one, from a passage you
// marked without selecting a sentence.
//
// The Kind is DELIBERATELY LEFT as bookmark rather than rewritten to highlight. Both are
// quotes for every purpose here, but a whole page and a hand-picked sentence are very
// different flashcards, and knowing which one you are looking at is worth keeping. The
// blockquote styles them apart for the same reason.
func (idx *readingIndex) Highlights() []ReadEvent {
	out := make([]ReadEvent, 0, len(idx.Annotations))
	for _, a := range idx.Annotations {
		if strings.TrimSpace(a.Text) != "" {
			out = append(out, a)
		}
	}
	return out
}

// Year returns the dates of one year, and the largest day in it (for scaling the grid).
func (idx *readingIndex) Year(year int) (map[string]*DaySummary, int) {
	prefix := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006")
	out, max := map[string]*DaySummary{}, 0
	for date, day := range idx.Days {
		if strings.HasPrefix(date, prefix) {
			out[date] = day
			if day.Seconds > max {
				max = day.Seconds
			}
		}
	}
	return out, max
}

// Years lists the years that have any activity, newest first.
func (idx *readingIndex) Years() []int {
	seen := map[int]bool{}
	for date := range idx.Days {
		if len(date) >= 4 {
			y, err := time.Parse("2006", date[:4])
			if err == nil {
				seen[y.Year()] = true
			}
		}
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}
