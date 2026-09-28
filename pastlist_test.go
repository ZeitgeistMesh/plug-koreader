package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"xf/controller/pkg/activity"
)

func TestKoreaderSaysWhatWasReadAndWhen(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	caller := "m@zm.is"
	tk := tenant(caller)
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) int64 { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Unix() }
	put := func(ev ReadEvent) {
		if err := a.events.Put(ctx, tk, ev.ID, &ev); err != nil {
			t.Fatal(err)
		}
	}
	// Page 10 read twice: counted once. 3 distinct pages, 5 minutes.
	put(ReadEvent{ID: "r1", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 10, StartTime: at(21, 0), Duration: 60})
	put(ReadEvent{ID: "r2", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 11, StartTime: at(21, 2), Duration: 120})
	put(ReadEvent{ID: "r3", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 10, StartTime: at(21, 5), Duration: 60})
	put(ReadEvent{ID: "r4", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 12, StartTime: at(21, 7), Duration: 60})
	put(ReadEvent{ID: "hl", Kind: KindHighlight, BookMD5: "b1", Title: "Dune", Text: "Fear is the mind-killer.", StartTime: at(21, 3)})
	// Outside the window, both sides.
	put(ReadEvent{ID: "old", Kind: KindRead, BookMD5: "b2", Title: "Emma", Page: 1, StartTime: at(-3, 0), Duration: 600})
	put(ReadEvent{ID: "new", Kind: KindRead, BookMD5: "b2", Title: "Emma", Page: 2, StartTime: at(24, 0), Duration: 600})

	out := a.dispatch(ctx, capPastList, map[string]any{"caller_user_id": caller,
		"from": day.Format(time.RFC3339), "to": day.Add(24 * time.Hour).Format(time.RFC3339)})
	r := activity.FromMeta(out)
	if len(r.Items) != 2 {
		t.Fatalf("items = %+v, want Dune's reading and one highlight (Emma is outside the window)", r.Items)
	}
	read, hl := r.Items[0], r.Items[1]
	if read.Verb != activity.Logged || read.Title != "Read: Dune — 3 pages" || read.Value != 5 || read.Unit != "min" ||
		!read.At.Equal(time.Unix(at(21, 0), 0)) || read.URL != "https://koreader.zm.is/day?d=2026-09-20" || read.Private {
		t.Fatalf("reading = %+v", read)
	}
	if hl.Verb != activity.Logged || hl.Title != "Highlighted a passage in Dune" || strings.Contains(hl.Title, "mind-killer") {
		t.Fatalf("highlight = %+v, want the book named and the passage kept out", hl)
	}
	tallies := map[string]float64{}
	for _, tl := range r.Tallies {
		tallies[tl.Name] = tl.Value
	}
	if tallies["pages read"] != 3 || tallies["minutes read"] != 5 {
		t.Fatalf("tallies = %+v, want 3 pages and 5 minutes", r.Tallies)
	}
	if _, bad := a.dispatch(ctx, capPastList, map[string]any{"caller_user_id": caller})["error"]; !bad {
		t.Fatal("a call without a window was answered")
	}
	// Another reader sees none of it.
	other := activity.FromMeta(a.dispatch(ctx, capPastList, map[string]any{"caller_user_id": "you@zm.is",
		"from": day.Format(time.RFC3339), "to": day.Add(24 * time.Hour).Format(time.RFC3339)}))
	if len(other.Items) != 0 {
		t.Fatalf("another tenant saw %+v", other.Items)
	}
}
