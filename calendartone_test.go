package main

import (
	"strings"
	"testing"
	"time"
)

// Owner dab02a73 (20:51, 16 Nov), on July's second week of the year page: "needs continuous
// colors for the same book". A book's tone was handed out per month in order of appearance, so
// the same book changed colour from one month to the next, and a second copy of it (another md5)
// was another bar in another colour. One book is one tone for the whole year, whatever month or
// copy it is read in.
func TestABookKeepsOneColourAcrossTheYear(t *testing.T) {
	ev := []ReadEvent{}
	add := func(md5, title string, month time.Month, day int) {
		at := time.Date(2026, month, day, 20, 0, 0, 0, time.UTC).Unix()
		ev = append(ev, ReadEvent{ID: md5 + title + time.Unix(at, 0).String(), Kind: KindRead, BookMD5: md5, Title: title, Page: day, StartTime: at, Duration: 900})
	}
	// June starts Emma; July reads Dune, then Emma again, then a second copy of Dune; August
	// reads Dune alone, so in August it is the first book of the month.
	add("e", "Emma", time.June, 10)
	for _, day := range []int{6, 7, 8} {
		add("d1", "Dune", time.July, day)
	}
	add("e", "Emma", time.July, 9)
	for _, day := range []int{14, 15} {
		add("d2", "Dune", time.July, day)
	}
	add("d2", "Dune", time.August, 3)
	idx := buildReadingIndex(ev, time.UTC)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	tones := map[string]map[int]bool{}
	for _, m := range []time.Month{time.June, time.July, time.August} {
		c := buildMonthCal(idx, time.UTC, time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC), now)
		for _, w := range c.Weeks {
			for _, b := range w.Bars {
				if tones[b.Title] == nil {
					tones[b.Title] = map[int]bool{}
				}
				tones[b.Title][b.Tone] = true
			}
		}
		if m == time.July && c.Books != 2 {
			t.Errorf("July counts %d books; two copies of Dune are one book", c.Books)
		}
	}
	if len(tones["Dune"]) != 1 || len(tones["Emma"]) != 1 {
		t.Fatalf("a book changes colour: %v", tones)
	}
	for d := range tones["Dune"] {
		if tones["Emma"][d] {
			t.Errorf("Dune and Emma, read side by side, share tone %d", d)
		}
	}
}

// pagelint on /year (integrity, 16 Nov): 820 corners off the house scale (4px day boxes and bars)
// and 46 one-day bar titles cut with "…", and a month scrolled to sat under the sticky header.
// Boxes and bars take the house corner (a 17px bar at 9px reads as round, which the scale
// allows); the rows stay fixed, so the full titles are the day's house hint, on whatever the
// pointer is over (the box, the day number, the ticks; a bar lets the pointer through).
func TestTheYearGridTakesTheHouseCornerAndHintsFullTitles(t *testing.T) {
	css := koCalCSS()
	if strings.Contains(css, "--r-chip,4px") || strings.Count(css, "border-radius:var(--r)") < 2 {
		t.Error("the day boxes or bars are off the house corner")
	}
	if !strings.Contains(yearCSS, "scroll-margin-top:") {
		t.Error("a month heading scrolls under the sticky header")
	}
	long := "A Short History of Nearly Everything"
	at := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC).Unix()
	ev := []ReadEvent{
		{ID: "1", Kind: KindRead, BookMD5: "s1", Title: long, Page: 1, StartTime: at, Duration: 900},
		{ID: "2", Kind: KindRead, BookMD5: "s2", Title: long, Page: 2, StartTime: at + 60, Duration: 900},
		{ID: "3", Kind: KindRead, BookMD5: "e", Title: "Emma", Page: 3, StartTime: at + 120, Duration: 900},
	}
	idx := buildReadingIndex(ev, time.UTC)
	c := buildMonthCal(idx, time.UTC, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	page := string(renderMonthCal(c))
	tip := `data-tip="` + long + "\nEmma" + `"`
	if n := strings.Count(page, tip); n != 3 {
		t.Errorf("the day's hint (both titles in full, once each) is on %d of box, number and ticks", n)
	}
	if strings.Contains(page, `class="ko-bar`) && strings.Contains(page, `title="`) {
		t.Error("a bar still carries a native title")
	}
}
