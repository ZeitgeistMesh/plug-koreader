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
