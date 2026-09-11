package main

import (
	"context"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zoneinfo for %s unavailable: %v", name, err)
	}
	return loc
}

// TestPagesAreCountedDistinctly: KOReader writes one row per (page, visit), so a page read
// twice is two rows. Summing rows reports a day where you re-read a chapter as twice the
// pages you turned, which is a number the reader knows is wrong the moment they see it.
func TestPagesAreCountedDistinctly(t *testing.T) {
	const base = int64(1757000000) // 2025-09-04
	idx := buildReadingIndex([]ReadEvent{
		{ID: "a", Kind: KindRead, BookMD5: "b1", Title: "A Book", Page: 10, StartTime: base, Duration: 60},
		{ID: "b", Kind: KindRead, BookMD5: "b1", Title: "A Book", Page: 10, StartTime: base + 300, Duration: 40},
		{ID: "c", Kind: KindRead, BookMD5: "b1", Title: "A Book", Page: 11, StartTime: base + 600, Duration: 50},
	}, time.UTC)

	date := time.Unix(base, 0).UTC().Format("2006-01-02")
	day := idx.Days[date]
	if day == nil {
		t.Fatalf("no summary for %s", date)
	}
	if day.Pages != 2 {
		t.Errorf("pages = %d, want 2 (page 10 was read twice)", day.Pages)
	}
	// Time, unlike pages, DOES accumulate across visits — re-reading a page still cost
	// you those seconds.
	if day.Seconds != 150 {
		t.Errorf("seconds = %d, want 150", day.Seconds)
	}
	if len(day.Books) != 1 || day.Books[0].Pages != 2 {
		t.Errorf("per-book pages wrong: %+v", day.Books)
	}
}

// TestDaysAreBucketedInTheDevicesZone: the reading day is the day the READER SAW on the
// e-reader's clock, not the day the account's timezone would make it.
//
// This replaced two earlier answers here — converting to the user's zone, then a 04:00
// cutoff to undo what that broke — and the owner's own 65-day run is what settled it. See
// the note on readingDate.
func TestDaysAreBucketedInTheDevicesZone(t *testing.T) {
	// The real rows behind the report: 23:07-23:18 UTC on 11 August. The reader's Kobo
	// runs UTC and showed "23:07 on the 11th", and that is the day they remember.
	when := time.Date(2026, 8, 11, 23, 7, 59, 0, time.UTC)
	idx := buildReadingIndex([]ReadEvent{
		{ID: "a", Kind: KindRead, BookMD5: "b", Title: "Compassion in Action", Page: 312, StartTime: when.Unix(), Duration: 95},
	}, time.UTC)
	if _, ok := idx.Days["2026-08-11"]; !ok {
		t.Errorf("late-night reading left the day it was done on; days = %v", idx.Dates)
	}

	// A device that IS configured gets its own offset honoured rather than UTC.
	plusTwo := time.FixedZone("device", 2*3600)
	idxTZ := buildReadingIndex([]ReadEvent{
		{ID: "a", Kind: KindRead, BookMD5: "b", Page: 1, StartTime: when.Unix(), Duration: 95},
	}, plusTwo)
	if _, ok := idxTZ.Days["2026-08-12"]; !ok {
		t.Errorf("23:07 UTC is 01:07 the next day at +02:00, and the device's clock is "+
			"what counts; days = %v", idxTZ.Dates)
	}
}

// TestNoCutoffShiftsADayBackwards is the regression test for the 04:00 cutoff this file
// used to carry.
//
// It was added to stop late-night reading falling into the next morning, and it did — by
// dragging an EARLY-morning session back into the previous day. Measured against the
// owner's account: a single session at 01:27 on 30 August became the 29th, so the calendar
// showed reading on the one day they were certain they had read none, and a gap on the day
// they resumed. A rule that has to lie about one day to tell the truth about another is
// the wrong rule; the timezone was the actual fault.
func TestNoCutoffShiftsADayBackwards(t *testing.T) {
	early := time.Date(2026, 8, 30, 1, 27, 0, 0, time.UTC)
	idx := buildReadingIndex([]ReadEvent{
		{ID: "a", Kind: KindRead, BookMD5: "b", Page: 1, StartTime: early.Unix(), Duration: 60},
	}, time.UTC)

	if _, ok := idx.Days["2026-08-30"]; !ok {
		t.Errorf("01:27 on the 30th must be the 30th; days = %v", idx.Dates)
	}
	if _, ok := idx.Days["2026-08-29"]; ok {
		t.Errorf("a cutoff has crept back in: the 29th was a day with NO reading, and it "+
			"is showing reading dragged back from the 30th")
	}
}

// TestAnnotationsDoNotCountAsReading: a highlight has no duration and turns no page, so it
// must not inflate the day's minutes or the calendar's intensity.
func TestAnnotationsDoNotCountAsReading(t *testing.T) {
	const base = int64(1757000000)
	idx := buildReadingIndex([]ReadEvent{
		{ID: "r", Kind: KindRead, BookMD5: "b", Page: 4, StartTime: base, Duration: 120},
		{ID: "h", Kind: KindHighlight, BookMD5: "b", Page: 4, StartTime: base + 10, Text: "a quote"},
		{ID: "m", Kind: KindBookmark, BookMD5: "b", Page: 5, StartTime: base + 20},
	}, time.UTC)

	day := idx.Days[time.Unix(base, 0).UTC().Format("2006-01-02")]
	if day.Seconds != 120 || day.Pages != 1 {
		t.Errorf("annotations leaked into the reading totals: %d s, %d pages", day.Seconds, day.Pages)
	}
	if day.Highlights != 1 || day.Bookmarks != 1 {
		t.Errorf("annotations not counted: %d highlights, %d bookmarks", day.Highlights, day.Bookmarks)
	}
	if idx.TotalRead != 1 {
		t.Errorf("TotalRead = %d, want 1", idx.TotalRead)
	}
	if got := len(idx.Highlights()); got != 1 {
		t.Errorf("Highlights() = %d, want 1 — a bookmark has no quote to review", got)
	}
}

// TestIntensityIsBandedNotRelative: a single six-hour Sunday must not make three hundred
// honest half-hours render as the palest shade.
func TestIntensityIsBandedNotRelative(t *testing.T) {
	for _, tc := range []struct {
		minutes, want int
	}{{0, 0}, {5, 1}, {14, 1}, {15, 2}, {44, 2}, {45, 3}, {119, 3}, {120, 4}, {600, 4}} {
		if got := intensity(tc.minutes * 60); got != tc.want {
			t.Errorf("intensity(%d min) = %d, want %d", tc.minutes, got, tc.want)
		}
	}
}

// TestReviewScheduleAdvancesAndResets pins the SM-2 behaviour that matters: success pushes
// the card away, a lapse brings it back to tomorrow, and ease never falls below the floor.
func TestReviewScheduleAdvancesAndResets(t *testing.T) {
	day := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	c := newCard("h1", day.Format("2006-01-02"))

	c = c.review(GradeGood, day)
	if c.Interval != 1 || c.DueOn != "2026-09-12" {
		t.Fatalf("first good review: interval %d due %s, want 1 / 2026-09-12", c.Interval, c.DueOn)
	}
	c = c.review(GradeGood, day.AddDate(0, 0, 1))
	if c.Interval != 6 {
		t.Fatalf("second good review: interval %d, want 6", c.Interval)
	}
	c = c.review(GradeGood, day.AddDate(0, 0, 7))
	if c.Interval <= 6 {
		t.Fatalf("third good review did not extend: %d", c.Interval)
	}

	lapsed := c.review(GradeForgot, day.AddDate(0, 0, 30))
	if lapsed.Interval != 1 {
		t.Errorf("a lapse must come back tomorrow, got interval %d", lapsed.Interval)
	}
	if lapsed.Reps != 0 || lapsed.Lapses != 1 {
		t.Errorf("lapse bookkeeping wrong: reps %d lapses %d", lapsed.Reps, lapsed.Lapses)
	}

	// Ease floor: many lapses must not drive it to zero and freeze the card at 1 day.
	hard := newCard("h2", "2026-09-11")
	for i := 0; i < 20; i++ {
		hard = hard.review(GradeForgot, day)
	}
	if hard.Ease < minEase {
		t.Errorf("ease fell below the floor: %v", hard.Ease)
	}
}

// TestDueCardsPrefersOverdueOverNew: a fresh sync can add hundreds of quotes at once, and
// burying the ones already in rotation under them is how a review queue stops being used.
func TestDueCardsPrefersOverdueOverNew(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	const base = int64(1757000000)

	idx := buildReadingIndex([]ReadEvent{
		{ID: "old", Kind: KindHighlight, BookMD5: "b", StartTime: base, Text: "an old quote"},
		{ID: "new", Kind: KindHighlight, BookMD5: "b", StartTime: base + 99999, Text: "a new quote"},
	}, time.UTC)

	seen := Card{ID: "old", Ease: startEase, Interval: 3, Reps: 2, DueOn: "2026-09-01"}
	if err := a.cards.Put(ctx, tk, seen.ID, &seen); err != nil {
		t.Fatal(err)
	}

	due, total, err := a.dueCards(ctx, tk, idx, "2026-09-11", 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if due[0].Card.ID != "old" || due[0].New {
		t.Errorf("the overdue card must come first, got %q (new=%v)", due[0].Card.ID, due[0].New)
	}
}

// TestSuspendedCardsLeaveTheQueue.
func TestSuspendedCardsLeaveTheQueue(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")

	idx := buildReadingIndex([]ReadEvent{
		{ID: "q", Kind: KindHighlight, BookMD5: "b", StartTime: 1757000000, Text: "a quote"},
	}, time.UTC)

	if _, total, _ := a.dueCards(ctx, tk, idx, "2026-09-11", 0); total != 1 {
		t.Fatalf("expected the new quote to be due, total=%d", total)
	}
	if err := a.suspendCard(ctx, tk, "q", time.UTC); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := a.dueCards(ctx, tk, idx, "2026-09-11", 0); total != 0 {
		t.Errorf("a suspended quote is still being offered")
	}
}
