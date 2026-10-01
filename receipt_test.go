package main

import (
	"context"
	"testing"
)

// TestSyncReceiptForTheOpenBook pins what the Sync message is built from. The device also
// syncs on its own whenever wifi connects, so a press of Sync after a night of
// highlighting finds nothing new — and "0 new" read as if the highlights were lost. The
// reply now splits its counts by kind and, for the open book, says how many annotations
// the server actually HOLDS and when the newest arrived.
func TestSyncReceiptForTheOpenBook(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	batch := []ReadEvent{
		{ID: "r1", Kind: KindRead, BookMD5: "edge", StartTime: 1700000000},
		{ID: "h1", Kind: KindHighlight, BookMD5: "edge", StartTime: 1700000100, Text: "small daily disciplines"},
		{ID: "h2", Kind: KindHighlight, BookMD5: "edge", StartTime: 1700000200, Text: "compounded over time"},
		{ID: "h2", Kind: KindHighlight, BookMD5: "edge", StartTime: 1700000200, Text: "compounded over time"},
		{ID: "o1", Kind: KindHighlight, BookMD5: "other", StartTime: 1700000300, Text: "another book"},
	}

	first := a.storeEvents(ctx, tk, dev, batch)
	if first.ReadingNew != 1 || first.AnnotationsNew != 3 || first.AnnotationsUpdated != 0 {
		t.Fatalf("first sync: reading_new=%d annotations_new=%d annotations_updated=%d, want 1/3/0",
			first.ReadingNew, first.AnnotationsNew, first.AnnotationsUpdated)
	}

	// The press that found nothing new: every count is zero, and the receipt still
	// proves both of the open book's highlights are held. The twin h2 counts once.
	again := a.storeEvents(ctx, tk, dev, batch)
	if again.ReadingNew != 0 || again.AnnotationsNew != 0 || again.AnnotationsUpdated != 0 {
		t.Fatalf("resend: reading_new=%d annotations_new=%d annotations_updated=%d, want all 0",
			again.ReadingNew, again.AnnotationsNew, again.AnnotationsUpdated)
	}
	got := a.receiptFor(ctx, tk, "edge", batch)
	if got == nil || got.Held != 2 {
		t.Fatalf("receipt for the open book = %+v, want held=2 (h1, h2; not the read row, not the other book)", got)
	}
	if got.NewestReceived == 0 {
		t.Fatalf("receipt has no arrival time — the reader cannot see WHEN the highlights got there")
	}

	// A note added later is an update, and is counted as one.
	edited := []ReadEvent{{ID: "h1", Kind: KindHighlight, BookMD5: "edge", StartTime: 1700000100,
		Text: "small daily disciplines", Note: "this is the whole book"}}
	if res := a.storeEvents(ctx, tk, dev, edited); res.AnnotationsUpdated != 1 || res.AnnotationsNew != 0 {
		t.Fatalf("note added: annotations_updated=%d annotations_new=%d, want 1/0",
			res.AnnotationsUpdated, res.AnnotationsNew)
	}

	if r := a.receiptFor(ctx, tk, "never-synced", batch); r == nil || r.Held != 0 {
		t.Fatalf("receipt for a book with nothing held = %+v, want held=0", r)
	}
	if r := a.receiptFor(ctx, tk, "", batch); r != nil {
		t.Fatalf("no open book must give no receipt, got %+v", r)
	}
}

// TestDisplayTitleDropsAPlaceholderAuthor: "Unknown - The Slight Edge" is a file name with
// an empty author slot, and showed as "Unknown". A real title with a dash is left alone.
func TestDisplayTitleDropsAPlaceholderAuthor(t *testing.T) {
	for in, want := range map[string]string{
		"Unknown - The Slight Edge":          "The Slight Edge",
		"N/A - Deep Work":                    "Deep Work",
		"Grog - CIA Book of Dirty Tricks":    "Grog - CIA Book of Dirty Tricks",
		"Star Wars - A New Hope":             "Star Wars - A New Hope",
		"Unknown - ":                         "Unknown -",
		"":                                   "Untitled",
		"  ":                                 "Untitled",
	} {
		if got := displayTitle(in); got != want {
			t.Errorf("displayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
