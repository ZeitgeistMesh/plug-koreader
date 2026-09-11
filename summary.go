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

// capTimeTimezoneGet is plug-time's read-only zone capability, on its free peer seam.
const capTimeTimezoneGet = "time.timezone.get"

const (
	userTZTTL         = 5 * time.Minute
	userTZFailTTL     = 30 * time.Second
	userTZTimeoutSecs = 3
)

// dayCutoffHour is when a READING DAY rolls over — 04:00, not midnight.
//
// Reading is the thing people do last before sleeping, so midnight cuts a single session in
// half and files the end of Tuesday evening under Wednesday. Reported against a real day:
// 23:07–23:18 UTC on Tuesday 11 August is 00:07 BST, and the calendar showed Tuesday as a
// day with no reading on it while the reader plainly remembered reading.
//
// plug-diary made exactly this decision for exactly this reason (its DayCutoff, defaulting
// to 03:00, where "a moment before the cutoff counts as the PREVIOUS day"), so this is the
// mesh's existing idiom rather than a local fudge. 04:00 because a book at 03:30 is still
// last night's reading.
//
// NOT a timezone workaround. The device's clock is correct UTC and the zone conversion is
// correct; the cutoff is a separate, honest statement about what a "day of reading" means.
const dayCutoffHour = 4

// readingDate is the day an instant belongs to, in the reader's zone, honouring the cutoff.
func readingDate(ts int64, loc *time.Location) string {
	return time.Unix(ts, 0).In(loc).Add(-dayCutoffHour * time.Hour).Format("2006-01-02")
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
	TotalRead   int
	BuiltAt     time.Time
	Zone        string
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
	loc := a.userLoc(ctx, caller)
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

	// Newest annotation first — the review list's natural order.
	sort.Slice(idx.Annotations, func(i, j int) bool {
		return idx.Annotations[i].StartTime > idx.Annotations[j].StartTime
	})
	return idx
}

// Highlights returns only the annotations carrying a quote. A bookmark marks a place and
// has nothing to put on a card.
func (idx *readingIndex) Highlights() []ReadEvent {
	out := make([]ReadEvent, 0, len(idx.Annotations))
	for _, a := range idx.Annotations {
		if a.Kind == KindHighlight && strings.TrimSpace(a.Text) != "" {
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

// ---------- the caller's timezone ----------

type tzEntry struct {
	loc     *time.Location
	expires time.Time
}

type tzCache struct {
	mu sync.Mutex
	m  map[string]tzEntry
}

func newTZCache() *tzCache { return &tzCache{m: map[string]tzEntry{}} }

// userLoc resolves the caller's zone via plug-time, cached, and NEVER fails: every error
// path yields UTC. Deliberately UTC and not time.Local — a dev box that silently disagrees
// with production is how this class of bug stays invisible.
func (a *app) userLoc(ctx context.Context, caller string) *time.Location {
	if caller == "" || a.rt == nil {
		return time.UTC
	}
	a.tz.mu.Lock()
	e, ok := a.tz.m[caller]
	a.tz.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.loc
	}

	loc, ttl := time.UTC, userTZFailTTL
	cctx, cancel := context.WithTimeout(ctx, userTZTimeoutSecs*time.Second)
	defer cancel()
	if ev, err := a.rt.CallPeer(cctx, a.timeJID, capTimeTimezoneGet, caller, map[string]any{}); err == nil {
		if name := strings.TrimSpace(strFromMeta(ev.Result.Meta, "timezone", "tz")); name != "" {
			if l, lerr := time.LoadLocation(name); lerr == nil {
				loc, ttl = l, userTZTTL
			}
		}
	}
	a.tz.mu.Lock()
	a.tz.m[caller] = tzEntry{loc: loc, expires: time.Now().Add(ttl)}
	a.tz.mu.Unlock()
	return loc
}

// strFromMeta reads the first non-empty string among several candidate keys, because a
// peer's field name is a contract this plug does not own.
func strFromMeta(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
