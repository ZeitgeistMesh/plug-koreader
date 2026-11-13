package main

import (
	"strings"
	"testing"

	"xf/controller/pkg/webui"
)

// Owner, plan 26856ccd: the EPUB link sat tight against Read and was drawn in another theme. Read and
// every download are the same house button, in one row.
func TestCatalogFormatsAreOneRowOfHouseButtons(t *testing.T) {
	e := CatalogEntry{ID: "urn:1", Title: "Dune", Files: []BookFile{{Type: "application/epub+zip", Href: "/x.epub"}}}
	rows, _ := catalogRows([]CatalogEntry{e}, webui.ListQuery{Page: 1, PageSize: 25})
	h := string(rows[0]["formats"])
	if !strings.HasPrefix(h, `<div class="field-row">`) || strings.Count(h, `class="btn `) != 2 {
		t.Fatalf("want Read and EPUB as two buttons in one field-row: %s", h)
	}
	if strings.Contains(h, "<a href=") && !strings.Contains(h, `class="btn`) {
		t.Errorf("a bare link crept back: %s", h)
	}
	if !strings.Contains(h, ">EPUB<") && !strings.Contains(h, "EPUB</") {
		t.Errorf("download button lost its label: %s", h)
	}
}
