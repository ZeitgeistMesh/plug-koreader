package main

import (
	"context"
	"testing"
	"time"
)

// One row per book per day, starting at the first page turned, with distinct pages.
func TestReadingSessionsGiveStartMinutesAndDistinctPages(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	caller := "m@zm.is"
	tk := tenant(caller)
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) int64 { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Unix() }
	for _, ev := range []ReadEvent{
		{ID: "r1", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 10, StartTime: at(7, 30), Duration: 120},
		{ID: "r2", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 11, StartTime: at(7, 32), Duration: 180},
		{ID: "r3", Kind: KindRead, BookMD5: "b1", Title: "Dune", Page: 10, StartTime: at(7, 36), Duration: 60},
		{ID: "r4", Kind: KindRead, BookMD5: "b2", Title: "Emma", Page: 1, StartTime: at(21, 0), Duration: 600},
		{ID: "hl", Kind: KindHighlight, BookMD5: "b1", Title: "Dune", Text: "secret passage", StartTime: at(7, 33)},
	} {
		e := ev
		if err := a.events.Put(ctx, tk, e.ID, &e); err != nil {
			t.Fatal(err)
		}
	}
	out := a.dispatch(ctx, capReadingSessions, map[string]any{"caller_user_id": caller,
		"from": day.Format(time.RFC3339), "to": day.Add(24 * time.Hour).Format(time.RFC3339)})
	rows, _ := out["sessions"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("sessions = %#v", out)
	}
	d := rows[0]
	if d["book"] != "Dune" || d["start"] != "2026-09-20T07:30:00Z" || d["minutes"] != 6 || d["pages"] != 2 || d["date"] != "2026-09-20" {
		t.Fatalf("Dune row = %#v", d)
	}
	for _, r := range rows {
		for k, v := range r {
			if s, ok := v.(string); ok && s == "secret passage" {
				t.Fatalf("highlight text leaked in %s", k)
			}
		}
	}
}
