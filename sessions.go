package main

// sessions.go — what a reading habit reconciles against (plug-habits, activityreconcile.go).
//
// One row per book per reading day, the same grouping the Log uses (pastlist.go), but with the
// facts a habit's conditions are about: when the reading started (the first page turned that
// day, for a time-of-day window), how long it took, and how many distinct pages it covered.
// The book travels by title only; the highlighted text never leaves (ingest.go).

import (
	"context"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/activity"
	"xf/controller/pkg/dblib"
	"xf/controller/pkg/xats"
)

const capReadingSessions = "koreader.reading.sessions"

func readingSessionsCapability() xats.Capability {
	return xats.Capability{
		Name: capReadingSessions, Command: capReadingSessions, Title: "Reading Sessions",
		Description: "Each book you read on each day in a window: when you started, minutes read and distinct pages. " +
			"The evidence a reading habit is ticked from.",
		Icon: "fa-solid fa-book-open", RequiredScopes: []string{scopeUser}, ExposeToMCP: true,
		Security:    []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}},
		Annotations: map[string]any{"readOnlyHint": true},
		InputSchema: activity.InputSchema(),
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"sessions": map[string]any{"type": "array", "description": "one row per book per reading day", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"id":       map[string]any{"type": "string", "description": "stable id: book and local day"},
					"book":     map[string]any{"type": "string", "description": "the book's title"},
					"date":     map[string]any{"type": "string", "description": "the local reading day, YYYY-MM-DD"},
					"start":    map[string]any{"type": "string", "description": "the first page turned that day, RFC 3339"},
					"minutes":  map[string]any{"type": "integer", "description": "minutes read"},
					"duration": map[string]any{"type": "integer", "description": "seconds read"},
					"pages":    map[string]any{"type": "integer", "description": "distinct pages read"},
				}}},
			"count": map[string]any{"type": "integer", "description": "how many rows"},
		}},
	}
}

func (a *app) handleReadingSessions(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	from, to, err := activity.Window(meta)
	if err != nil {
		return map[string]any{"error": err.Error(), "body": err.Error()}
	}
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return map[string]any{"error": "could not read your reading", "body": "could not read your reading"}
	}
	loc := a.deviceLoc(ctx, tk)
	rows := readingSessions(idx, loc, from, to)
	return map[string]any{"sessions": rows, "count": len(rows)}
}

// readingSessions groups page turns in [from,to) by local day and book.
func readingSessions(idx *readingIndex, loc *time.Location, from, to time.Time) []map[string]any {
	type key struct{ date, md5 string }
	type agg struct {
		first   int64
		title   string
		seconds int
		pages   map[int]bool
	}
	groups := map[key]*agg{}
	lo, hi := from.Unix(), to.Unix()
	i := sort.Search(len(idx.Reads), func(i int) bool { return idx.Reads[i].StartTime >= lo })
	for ; i < len(idx.Reads) && idx.Reads[i].StartTime < hi; i++ {
		ev := idx.Reads[i]
		k := key{readingDate(ev.StartTime, loc), ev.BookMD5}
		g := groups[k]
		if g == nil {
			g = &agg{first: ev.StartTime, title: ev.Title, pages: map[int]bool{}}
			groups[k] = g
		}
		g.seconds += ev.Duration
		g.pages[ev.Page] = true
	}
	out := make([]map[string]any, 0, len(groups))
	for k, g := range groups {
		out = append(out, map[string]any{
			"id": k.md5 + ":" + k.date, "book": strings.TrimSpace(displayTitle(g.title)), "date": k.date,
			"start":   time.Unix(g.first, 0).UTC().Format(time.RFC3339),
			"minutes": DaySummary{Seconds: g.seconds}.Minutes(), "duration": g.seconds, "pages": len(g.pages),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["start"].(string) < out[j]["start"].(string) })
	return out
}
