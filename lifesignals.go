package main

// lifesignals.go — koreader's feed into life's KNOWLEDGE bucket (design §6, P4).
//
// Life's buckets were filled only by goals, and neither thing koreader knows is a goal:
// reading time this week is a MEASURE, not a target, and forcing it into one would put a
// fake progress bar on a dashboard. So koreader answers life's existing life.goals.list
// node with no goals and two SIGNALS — current readings, each with a tone koreader decides,
// because only koreader knows what its numbers mean:
//
//	reading_week   time and books this week; "good" when above the trailing four-week
//	               median, else "neutral"
//	quotes_month   highlights made in the last 30 days
//
// Computed from the cached reading index, so it never waits on anything. A "read 20 min a
// day" GOAL belongs to habits, not here.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"xf/controller/pkg/lifegoal"
	"xf/controller/pkg/xats"
)

const capLifeGoalsList = "life.goals.list"

func lifeGoalsCapability() xats.Capability {
	return xats.Capability{
		Name: capLifeGoalsList, Command: capLifeGoalsList, Title: "Reading for Life",
		Description:    "Your reading as signals for Life's knowledge bucket: time and books this week, and highlights this month.",
		RequiredScopes: []string{scopeUser},
		Internal:       true,
		Annotations:    map[string]any{"readOnlyHint": true},
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (a *app) handleLifeGoals(ctx context.Context, caller string) map[string]any {
	if caller == "" {
		return map[string]any{"error": "not signed in", "body": "not signed in"}
	}
	tk := tenant(caller)
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return map[string]any{"error": "could not read your reading", "body": "could not read your reading"}
	}
	sigs := readingSignals(idx, a.deviceLoc(ctx, tk), time.Now(), a.publicURL)
	return map[string]any{"goals": []any{}, "signals": sigs, "body": ""}
}

// readingSignals is the pure computation, so it is tested without a store.
func readingSignals(idx *readingIndex, loc *time.Location, now time.Time, publicURL string) []lifegoal.Signal {
	today := now.In(loc)
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	// Weeks start on Monday, the reader's week as the calendar draws it.
	monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))

	weekOf := func(start time.Time) (seconds int, books int) {
		seen := map[string]bool{}
		for d := 0; d < 7; d++ {
			date := start.AddDate(0, 0, d)
			if date.After(day) {
				break
			}
			if s := idx.Days[date.Format("2006-01-02")]; s != nil {
				seconds += s.Seconds
				for _, b := range s.Books {
					seen[b.BookMD5] = true
				}
			}
		}
		return seconds, len(seen)
	}
	thisSecs, thisBooks := weekOf(monday)
	var past []int
	for w := 1; w <= 4; w++ {
		s, _ := weekOf(monday.AddDate(0, 0, -7*w))
		past = append(past, s)
	}
	sort.Ints(past)
	median := (past[1] + past[2]) / 2
	tone, note := lifegoal.ToneNeutral, ""
	if thisSecs > median && thisSecs > 0 {
		tone, note = lifegoal.ToneGood, "Ahead of your usual week."
	}
	value := "Nothing yet this week"
	if thisSecs > 0 {
		value = fmt.Sprintf("%s · %d %s", hoursMinutes(thisSecs), thisBooks, plural(thisBooks, "book"))
	}
	week := lifegoal.Signal{Source: "koreader", Key: "reading_week", Bucket: "knowledge", Title: "Reading this week",
		Value: value, Tone: tone, Note: note, Since: monday.Format(time.RFC3339), DeepLink: publicURL + "/"}

	cutoff := now.AddDate(0, 0, -30).Unix()
	quotes := 0
	for _, h := range idx.Highlights() {
		if h.StartTime >= cutoff {
			quotes++
		}
	}
	month := lifegoal.Signal{Source: "koreader", Key: "quotes_month", Bucket: "knowledge", Title: "Highlights, last 30 days",
		Value: fmt.Sprintf("%d %s", quotes, plural(quotes, "highlight")), Tone: lifegoal.ToneNeutral,
		Since: now.AddDate(0, 0, -30).In(loc).Format(time.RFC3339), DeepLink: publicURL + "/quotes"}
	return []lifegoal.Signal{week, month}
}

// hoursMinutes is "4 h 20 min", or "35 min" under an hour.
func hoursMinutes(seconds int) string {
	m := seconds / 60
	if m < 60 {
		return fmt.Sprintf("%d min", maxInt(m, 1))
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}
