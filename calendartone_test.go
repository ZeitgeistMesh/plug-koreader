package main

import (
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
