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
