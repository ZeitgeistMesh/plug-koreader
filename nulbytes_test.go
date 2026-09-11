package main

import (
	"context"
	"strings"
	"testing"
)

// TestPdfTitlesWithNulBytesAreStored is the regression test for 1,185 rows that vanished
// into a rejection count.
//
// KOReader reads a PDF's title straight out of its metadata, where it commonly carries a
// trailing NUL. jsonb has no representation for U+0000, so every Put for such a book
// failed and the row was dropped. MEASURED on production: 24 books lost ALL of their
// rows, 200 books lost none, and the split was exactly whether the title held a control
// byte — which is what turned a plausible guess into a cause.
func TestPdfTitlesWithNulBytesAreStored(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	res := a.storeEvents(ctx, tk, dev, []ReadEvent{{
		ID: "pdf1", Kind: "read", StartTime: 1700000000,
		Title:   "Deep Work\x00",
		Authors: "Cal Newport\x00",
		Text:    "a highlight with a \x00 in it",
	}})

	if res.Rejected != 0 {
		t.Fatalf("rejected %d — a NUL in a PDF title must not cost the row (%s)",
			res.Rejected, res.RejectReason)
	}
	if res.Accepted != 1 {
		t.Fatalf("accepted %d, want 1", res.Accepted)
	}

	got, err := a.events.Get(ctx, tk, "pdf1")
	if err != nil || got == nil {
		t.Fatalf("event not stored: %v", err)
	}
	if strings.ContainsRune(got.Title, 0) || strings.ContainsRune(got.Authors, 0) ||
		strings.ContainsRune(got.Text, 0) {
		t.Error("a NUL survived into the stored document")
	}
	if got.Title != "Deep Work" || got.Authors != "Cal Newport" {
		t.Errorf("scrubbing changed more than the control byte: %q / %q", got.Title, got.Authors)
	}
}

// TestScrubKeepsRealText guards the other direction — the scrub must not eat legitimate
// content. A highlight spans lines, and this library has non-Latin titles.
func TestScrubKeepsRealText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Мастер и Маргарита", "Мастер и Маргарита"},
		{"line one\nline two\ttabbed", "line one\nline two\ttabbed"},
		{"plain", "plain"},
		{"", ""},
		{"bell\aand\x1bescape", "bellandescape"},
		{"trailing nul\x00", "trailing nul"},
	} {
		if got := scrubText(tc.in); got != tc.want {
			t.Errorf("scrubText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestARejectionAlwaysCarriesItsReason: a bare count is what hid the NUL failure — 1,185
// rows reported only as a number, small enough to look like rounding.
func TestARejectionAlwaysCarriesItsReason(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	// An event with no id is the rejection the handler raises on its own.
	res := a.storeEvents(ctx, tk, dev, []ReadEvent{{Kind: "read", StartTime: 1}})
	if res.Rejected != 1 {
		t.Fatalf("rejected %d, want 1", res.Rejected)
	}
	if !strings.Contains(res.Message, "rejected") {
		t.Errorf("message does not mention the rejection: %q", res.Message)
	}
}

// TestAChangedAnnotationOverwrites is the regression test for content that arrived, was
// recognised, and was thrown away.
//
// The annotation design rests on "re-send everything; an unchanged one is a duplicate and
// a changed one overwrites itself", and only the first half was implemented: the store
// skipped on ID ALONE. So a note added to a highlight months later, or a bookmark that had
// just had its page captured on the device, was reported back as "already had" and lost.
func TestAChangedAnnotationOverwrites(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	// A bookmark as it first syncs: a position, no text.
	first := a.storeEvents(ctx, tk, dev, []ReadEvent{
		{ID: "bm", Kind: KindBookmark, BookMD5: "b", Page: 82, StartTime: 1757000000},
	})
	if first.Accepted != 1 {
		t.Fatalf("accepted %d, want 1", first.Accepted)
	}

	// The same bookmark after the device captured its page.
	second := a.storeEvents(ctx, tk, dev, []ReadEvent{
		{ID: "bm", Kind: KindBookmark, BookMD5: "b", Page: 82, StartTime: 1757000000,
			Text: "the whole of page 82"},
	})
	if second.Updated != 1 {
		t.Errorf("updated %d, want 1 — the captured page was discarded as a duplicate "+
			"(%q)", second.Updated, second.Message)
	}
	got, err := a.events.Get(ctx, tk, "bm")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Text != "the whole of page 82" {
		t.Errorf("stored text = %q, want the captured page", got.Text)
	}

	// Sending it AGAIN unchanged is an ordinary duplicate, not a second write.
	third := a.storeEvents(ctx, tk, dev, []ReadEvent{
		{ID: "bm", Kind: KindBookmark, BookMD5: "b", Page: 82, StartTime: 1757000000,
			Text: "the whole of page 82"},
	})
	if third.Duplicate != 1 || third.Updated != 0 {
		t.Errorf("an unchanged resend was not a plain duplicate: %+v", third)
	}
}

// TestReadingRowsStillShortCircuit: a reading row cannot change, and comparing fields on
// every one of twenty thousand of them during a backfill would cost a great deal for an
// answer that is always "same".
func TestReadingRowsStillShortCircuit(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	ev := ReadEvent{ID: "r", Kind: KindRead, BookMD5: "b", Page: 1, StartTime: 1757000000, Duration: 60}
	if got := a.storeEvents(ctx, tk, dev, []ReadEvent{ev}); got.Accepted != 1 {
		t.Fatalf("accepted %d, want 1", got.Accepted)
	}
	// Even with a different title, a read row is a duplicate — never an update.
	ev.Title = "renamed"
	got := a.storeEvents(ctx, tk, dev, []ReadEvent{ev})
	if got.Duplicate != 1 || got.Updated != 0 {
		t.Errorf("a reading row took the update path: %+v", got)
	}
}
