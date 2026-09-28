package main

// pastlist.go — Koreader's answer to "what happened?" (plan c658295b, Past in Next).
//
// Reading is "logged": one item per book per reading day, at the first page turned in the
// window, titled with the book and the distinct pages read, valued in minutes. Pages are
// counted distinctly, exactly as the day page counts them (summary.go), so Past and the
// calendar never disagree. Highlights and bookmarks are "logged" at their own time, named
// by the book only — the passage itself stays off every shared surface (ingest.go).
// Tallies: pages read and minutes read in the window.
//
// NOT REPORTED: "book finished". KOReader's statistics rows say which page was open, not
// that a book was completed — reaching the last page is also what skimming to the index
// looks like — so there is no trustworthy completion event to report.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"xf/controller/pkg/activity"
	"xf/controller/pkg/dblib"
	"xf/controller/pkg/xats"
)

const capPastList = "koreader.past.list"

func pastListCapability() xats.Capability {
	return xats.Capability{
		Name: capPastList, Command: capPastList, Title: "What Happened in Koreader",
		Description: "What you read in a window: each book you read on each day (pages and minutes), and the passages you " +
			"highlighted or bookmarked (by book, never the text); plus pages and minutes read. The shared activity contract " +
			"Next's Past view reads.",
		Icon: "fa-solid fa-clock-rotate-left", RequiredScopes: []string{scopeUser},
		Security:     []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}},
		Annotations:  map[string]any{"readOnlyHint": true},
		InputSchema:  activity.InputSchema(),
		OutputSchema: activity.OutputSchema(),
	}
}

func (a *app) handlePastList(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	from, to, err := activity.Window(meta)
	if err != nil {
		return map[string]any{"error": err.Error(), "body": err.Error()}
	}
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		res := activity.Result{Partial: true, Reason: "could not read your reading"}
		return res.Map()
	}
	loc := a.deviceLoc(ctx, tk)
	var pages, seconds int
	reading := activity.SourceFunc(func(context.Context, dblib.TenantKey, time.Time, time.Time) ([]activity.Item, error) {
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
		out := make([]activity.Item, 0, len(groups))
		for k, g := range groups {
			pages += len(g.pages)
			seconds += g.seconds
			out = append(out, activity.Item{
				At: time.Unix(g.first, 0).UTC(), Verb: activity.Logged, Kind: "reading",
				Title: fmt.Sprintf("Read: %s — %d %s", orUntitled(g.title), len(g.pages), plural(len(g.pages), "page")),
				URL:   a.publicURL + "/day?d=" + url.QueryEscape(k.date),
				Value: float64(DaySummary{Seconds: g.seconds}.Minutes()), Unit: "min",
			})
		}
		return out, nil
	})
	notes := activity.SourceFunc(func(context.Context, dblib.TenantKey, time.Time, time.Time) ([]activity.Item, error) {
		var out []activity.Item
		for _, an := range idx.Annotations {
			at := time.Unix(an.StartTime, 0)
			if an.StartTime <= 0 || at.Before(from) || !at.Before(to) {
				continue
			}
			what, kind := "Highlighted a passage in ", "highlight"
			if an.Kind == KindBookmark {
				what, kind = "Bookmarked a page in ", "bookmark"
			}
			out = append(out, activity.Item{At: at.UTC(), Verb: activity.Logged, Kind: kind,
				Title: what + orUntitled(an.Title), URL: a.publicURL + "/quotes"})
		}
		return out, nil
	})
	sources := map[string]activity.Source{"reading": reading, "annotations": notes}
	res := activity.Gather(ctx, tk, from, to, nil, sources)
	res.Tallies = append(res.Tallies,
		activity.Tally{Name: "pages read", Value: float64(pages), Unit: "pages"},
		activity.Tally{Name: "minutes read", Value: float64(DaySummary{Seconds: seconds}.Minutes()), Unit: "min"})
	return res.Map()
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
