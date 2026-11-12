package main

import (
	"strings"
	"testing"
	"time"
)

func TestMonthCalendarBarsSpanDaysAndStackLanes(t *testing.T) {
	d := func(day, h int) int64 { return time.Date(2026, 11, day, h, 0, 0, 0, time.UTC).Unix() }
	ev := []ReadEvent{}
	add := func(md5, title string, day, h, secs int) {
		ev = append(ev, ReadEvent{ID: md5 + title + string(rune(day*30+h)), Kind: KindRead, BookMD5: md5, Title: title, Page: day*30 + h, StartTime: d(day, h), Duration: secs})
	}
	for _, day := range []int{2, 3, 4} { // Mon-Wed
		add("a", "Dune", day, 7, 600)
	}
	add("b", "Emma", 3, 21, 1500) // overlaps Dune on Tue
	idx := buildReadingIndex(ev, time.UTC)
	c := buildMonthCal(idx, time.UTC, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if c.Month != "2026-11" || c.Prev != "2026-10" || c.NextY != "2027-11" || c.Books != 2 {
		t.Fatalf("%+v", c)
	}
	var w calWeek
	for _, x := range c.Weeks {
		if len(x.Bars) > 0 {
			w = x
		}
	}
	if len(w.Bars) != 2 || w.Lanes != 2 {
		t.Fatalf("bars=%+v lanes=%d", w.Bars, w.Lanes)
	}
	if w.Bars[0].Title != "Dune" || w.Bars[0].Col != 1 || w.Bars[0].Span != 3 || w.Bars[0].Lane != 1 {
		t.Fatalf("dune bar %+v", w.Bars[0])
	}
	if w.Bars[1].Col != 2 || w.Bars[1].Lane != 2 || w.Bars[1].Tone == w.Bars[0].Tone {
		t.Fatalf("emma bar %+v", w.Bars[1])
	}
	if w.Days[1].Ticks[21] != 3 || w.Days[1].Ticks[7] != 2 || len(w.Days[1].Ticks) != 24 {
		t.Fatalf("ticks %+v", w.Days[1].Ticks)
	}
	h := string(renderMonthCal(c))
	for _, want := range []string{`ko-bar ko-a1 ko-n3 ko-r2`, `href="/day?d=2026-11-03"`, `/calendar?m=2026-10`, `2026-11`} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(h, `style=`) {
		t.Error("inline style")
	}
}

func TestCalendarCapabilityMatchesTheGrid(t *testing.T) {
	a := testApp()
	out := a.dispatch(t.Context(), capCalendarGet, map[string]any{"caller_user_id": "m@zm.is", "month": "2026-11"})
	if out["month"] != "2026-11" || out["error"] != nil {
		t.Fatalf("%#v", out)
	}
}

// Every cell is the same size: the date, four book rows, and a foot the height of one book row that
// holds the whole day, midnight to midnight, one tick per hour across the full cell width.
func TestMonthCalendarCellsAreOneSize(t *testing.T) {
	idx := buildReadingIndex([]ReadEvent{{ID: "e1", Kind: KindRead, BookMD5: "a", Title: "Dune", Page: 1,
		StartTime: time.Date(2026, 11, 3, 7, 0, 0, 0, time.UTC).Unix(), Duration: 600}}, time.UTC)
	c := buildMonthCal(idx, time.UTC, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	h := string(renderMonthCal(c))
	if !strings.Contains(h, `class="ko-ticks ko-a2 ko-r6"`) {
		t.Errorf("the histogram sits in the sixth row whatever the week holds")
	}
	if n := strings.Count(h[strings.Index(h, `ko-ticks`):], `<i class="ko-k`); n < 24 {
		t.Errorf("a day's histogram has one tick per hour, got %d", n)
	}
	css := koCalCSS()
	for _, want := range []string{`grid-template-rows:auto repeat(5,var(--ko-row))`, `.ko-ticks{display:grid;grid-template-columns:repeat(24,`} {
		if !strings.Contains(css, want) {
			t.Errorf("css missing %q", want)
		}
	}
}
