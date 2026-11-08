package main

import (
	"strings"
	"testing"
	"time"
)

func streakIdx(days ...string) *readingIndex {
	idx := &readingIndex{Days: map[string]*DaySummary{}}
	for _, d := range days {
		idx.Days[d] = &DaySummary{Date: d}
		idx.Dates = append(idx.Dates, d)
	}
	return idx
}

func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// Synced today: the confirmed value, plain. A day later with no sync: the same streak plus the
// day, starred, with the line saying how long and what was confirmed. A streak already broken
// when the reader synced stays at its plain value with no star.
func TestStreakIsOptimisticSinceTheLastSync(t *testing.T) {
	var days []string
	for d := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC); len(days) < 71; d = d.AddDate(0, 0, -1) {
		days = append(days, d.Format("2006-01-02"))
	}
	idx := streakIdx(days...)
	sync := at("2026-11-08T09:00:00Z")

	v := streakAsOf(idx, time.UTC, at("2026-11-08T20:00:00Z"), sync)
	if v.Shown != 71 || v.Optimistic || streakNote(v) != "" {
		t.Fatalf("synced today: %+v", v)
	}
	v = streakAsOf(idx, time.UTC, at("2026-11-09T08:00:00Z"), sync)
	if v.Shown != 72 || !v.Optimistic || v.Confirmed != 71 || streakNote(v) != "1 day since sync · last confirmed 71" {
		t.Fatalf("one day on: %+v %q", v, streakNote(v))
	}
	v = streakAsOf(idx, time.UTC, at("2026-11-11T08:00:00Z"), sync)
	if v.Shown != 74 || streakNote(v) != "3 days since sync · last confirmed 71" {
		t.Fatalf("three days on: %+v %q", v, streakNote(v))
	}

	// The streak ended well before the sync: broken at sync time, so 0 and no star.
	broken := streakIdx("2026-11-01", "2026-11-02", "2026-11-03")
	v = streakAsOf(broken, time.UTC, at("2026-11-10T08:00:00Z"), at("2026-11-08T09:00:00Z"))
	if v.Shown != 0 || v.Optimistic || strings.TrimSpace(streakNote(v)) != "" {
		t.Fatalf("broken at sync: %+v", v)
	}

	// Alive at sync because it ended the day before the sync.
	y := streakIdx("2026-11-06", "2026-11-07")
	v = streakAsOf(y, time.UTC, at("2026-11-09T08:00:00Z"), at("2026-11-08T09:00:00Z"))
	if v.Shown != 3 || !v.Optimistic {
		t.Fatalf("ended the day before sync: %+v", v)
	}

	// No sync recorded: the plain value.
	v = streakAsOf(idx, time.UTC, at("2026-11-09T08:00:00Z"), time.Time{})
	if v.Optimistic {
		t.Fatalf("no sync time: %+v", v)
	}
}
