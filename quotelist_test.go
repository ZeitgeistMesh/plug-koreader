package main

import (
	"context"
	"strings"
	"testing"
)

func TestQuoteListReturnsOneBooksTextInReadingOrder(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	caller := "m@zm.is"
	tk := tenant(caller)
	put := func(ev ReadEvent) {
		if err := a.events.Put(ctx, tk, ev.ID, &ev); err != nil {
			t.Fatal(err)
		}
	}
	put(ReadEvent{ID: "h2", Kind: KindHighlight, BookMD5: "se", Title: "Unknown - The Slight Edge", Page: 40, StartTime: 300, Text: "Easy to do, easy not to do."})
	put(ReadEvent{ID: "h1", Kind: KindHighlight, BookMD5: "se", Title: "Unknown - The Slight Edge", Page: 12, StartTime: 900, Text: "Simple daily disciplines.", Note: "yes"})
	put(ReadEvent{ID: "b1", Kind: KindBookmark, BookMD5: "se", Title: "Unknown - The Slight Edge", Page: 55, StartTime: 100})
	put(ReadEvent{ID: "d1", Kind: KindHighlight, BookMD5: "aa01", Title: "Dune", Page: 3, StartTime: 50, Text: "Fear is the mind-killer."})
	put(ReadEvent{ID: "d2", Kind: KindHighlight, BookMD5: "aa02", Title: "Dune Messiah", Page: 3, StartTime: 60, Text: "x"})
	put(ReadEvent{ID: "r1", Kind: KindRead, BookMD5: "se", Title: "Unknown - The Slight Edge", Page: 12, StartTime: 80, Duration: 30})

	call := func(who string, m map[string]any) map[string]any {
		m["caller_user_id"] = who
		return a.dispatch(ctx, capQuoteList, m)
	}

	out := call(caller, map[string]any{"book": "slight edge"})
	if out["error"] != nil {
		t.Fatalf("error: %v", out)
	}
	items := out["items"].([]quoteItem)
	if len(items) != 3 || items[0].Page != 12 || items[1].Page != 40 || items[2].Page != 55 {
		t.Fatalf("items = %+v, want the 3 annotations by page and no reading rows", items)
	}
	if items[0].Text != "Simple daily disciplines." || items[0].Note != "yes" || items[2].Kind != KindBookmark {
		t.Fatalf("items = %+v, want the text and note returned", items)
	}
	if b := out["book"].(quoteBook); b.Title != "The Slight Edge" || b.Highlights != 2 || b.Bookmarks != 1 || b.Notes != 1 {
		t.Fatalf("book = %+v", b)
	}

	if out := call(caller, map[string]any{"book": "dun"}); out["error"] == nil || len(out["books"].([]quoteBook)) != 2 ||
		!strings.Contains(out["body"].(string), "more than one") {
		t.Fatalf("an ambiguous title was answered: %+v", out)
	}
	if out := call(caller, map[string]any{"book": "dune"}); out["error"] != nil || out["book"].(quoteBook).MD5 != "aa01" {
		t.Fatalf("an exact title lost to a longer one: %+v", out)
	}
	if out := call(caller, map[string]any{"book": "aa02"}); out["error"] != nil || out["book"].(quoteBook).Title != "Dune Messiah" {
		t.Fatalf("an md5 was not resolved: %+v", out)
	}
	if out := call(caller, map[string]any{}); out["count"] != 3 {
		t.Fatalf("listing = %+v, want 3 annotated books", out)
	}
	if out := call(caller, map[string]any{"book": "slight", "limit": float64(1)}); out["count"] != 1 || out["total"] != 3 {
		t.Fatalf("limit ignored: %+v", out)
	}

	// Another reader sees none of it.
	if out := call("you@zm.is", map[string]any{"book": "slight edge"}); out["error"] == nil {
		t.Fatalf("another tenant read the quotes: %+v", out)
	}
}

// The export pages by (ReceivedAt, id): a batch stamped in one second and cut by the limit
// must resume where it was cut, and an overwritten highlight (a later ReceivedAt) comes
// round again so the mirror sees the change.
func TestQuoteExportPagesByArrivalAndResumes(t *testing.T) {
	evs := []ReadEvent{
		{ID: "b", Kind: KindHighlight, Text: "two", ReceivedAt: "2026-10-01T10:00:00Z", StartTime: 1757000000},
		{ID: "a", Kind: KindHighlight, Text: "one", ReceivedAt: "2026-10-01T10:00:00Z"},
		{ID: "c", Kind: KindHighlight, Text: "three", ReceivedAt: "2026-10-01T10:00:00Z"},
		{ID: "m", Kind: KindBookmark, ReceivedAt: "2026-10-01T10:00:00Z"},
		{ID: "z", Kind: KindHighlight, Text: "edited", ReceivedAt: "2026-10-02T09:00:00Z"},
	}
	page1, next, more := quoteExport(evs, "", 2)
	if len(page1) != 2 || page1[0].ID != "a" || page1[1].ID != "b" || !more {
		t.Fatalf("page 1 = %+v more=%v", page1, more)
	}
	if page1[0].Ref != "koreader:a" || page1[1].AnnotatedAt == "" {
		t.Errorf("an item lacks its ref or its time: %+v", page1)
	}
	page2, next2, more2 := quoteExport(evs, next, 2)
	if len(page2) != 2 || page2[0].ID != "c" || page2[1].ID != "z" || more2 {
		t.Fatalf("page 2 = %+v more=%v — the tied batch was not resumed where it was cut", page2, more2)
	}
	if rest, again, _ := quoteExport(evs, next2, 2); len(rest) != 0 || again != next2 {
		t.Errorf("past the end: %d items, cursor %q", len(rest), again)
	}
}

func TestQuoteExportIsInternal(t *testing.T) {
	c := quoteExportCapability()
	if !c.Internal || c.ExposeToMCP {
		t.Error("koreader.quote.export is a feed between plugs and must stay off MCP")
	}
}
