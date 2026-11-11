package main

import (
	"context"
	"fmt"
	"testing"
)

// The list has no upper bound: 5000 was the old cap, and a list well past it is saved in slices,
// read back whole, and replaced by a newer one without leaving the old slices behind.
func TestALongCatalogIsKeptInChunksAndReadBackWhole(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("m@zm.is")
	mk := func(n int, tag string) *Catalog {
		c := &Catalog{ID: catalogID, SourceURL: "https://b.example.com", RefreshedAt: nowRFC()}
		for i := 0; i < n; i++ {
			c.Entries = append(c.Entries, CatalogEntry{ID: fmt.Sprintf("%s-%d", tag, i), Title: fmt.Sprintf("Book %05d", i)})
		}
		return c
	}
	if err := a.saveCatalog(ctx, tk, mk(12345, "a")); err != nil {
		t.Fatal(err)
	}
	got := a.loadCatalog(ctx, tk)
	if got == nil || len(got.Entries) != 12345 || got.Entries[12344].ID != "a-12344" {
		t.Fatalf("read back %v", got)
	}
	chunks, _ := a.chunks.List(ctx, tk)
	if want := (12345 + chunkLen - 1) / chunkLen; len(chunks) != want {
		t.Errorf("%d chunks stored, want %d", len(chunks), want)
	}
	if err := a.saveCatalog(ctx, tk, mk(10, "b")); err != nil {
		t.Fatal(err)
	}
	if got = a.loadCatalog(ctx, tk); len(got.Entries) != 10 || got.Entries[0].ID != "b-0" {
		t.Errorf("the newer list was not read: %d entries", len(got.Entries))
	}
	if chunks, _ = a.chunks.List(ctx, tk); len(chunks) != 1 {
		t.Errorf("%d chunks left after a shorter list replaced a longer one", len(chunks))
	}
	a.dropCatalog(ctx, tk)
	if a.loadCatalog(ctx, tk) != nil {
		t.Error("the list survived being dropped")
	}
	if chunks, _ = a.chunks.List(ctx, tk); len(chunks) != 0 {
		t.Errorf("%d chunks survived being dropped", len(chunks))
	}
}

func TestTheSavedListRemembersItsCount(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("m@zm.is")
	old := &Catalog{ID: catalogID, SourceURL: "https://b.example.com", RefreshedAt: nowRFC()}
	for i := 0; i < 900; i++ {
		old.Entries = append(old.Entries, CatalogEntry{ID: fmt.Sprint(i), Title: "t"})
	}
	if err := a.saveCatalog(ctx, tk, old); err != nil {
		t.Fatal(err)
	}
	cur := a.loadCatalog(ctx, tk)
	if len(cur.Entries) != 900 || cur.Count != 900 {
		t.Fatalf("%d entries, count %d", len(cur.Entries), cur.Count)
	}
}
