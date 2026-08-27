package main

// webshape_test.go — the two defects this plug was reported for, pinned against the
// RENDERED page.
//
// Three reports arrived on 2026-08-27, all against this plug's two hand-written
// surfaces, and all of the same two shapes:
//
//	/addon  "buttons are touching and they should not be"   (twice)
//	/pair   "overflow"
//
// Neither is a koreader idea. Both are the shapes the shared layer already has a
// component for and STYLEGUIDE.md already forbids, which is why the guards here are
// written as GENERAL properties of the page rather than as assertions about the
// particular buttons and the particular table that were reported:
//
//	1. spacing belongs to the CONTAINER (§3). Two controls emitted adjacently — the
//	   `%s%s` shape — are separated by whatever whitespace happened to sit between
//	   them in the format string, which here was none.
//	2. a table needs an overflow container at EVERY width. BaseCSS gives a bare table
//	   element one only inside @media(max-width:640px), so above that it takes its
//	   natural width and grows out of the card that holds it. That is precisely what
//	   the 1916px screenshot showed.
//
// WHY THESE ASSERT ON THE RENDERED BYTES AND NOT ON THIS PACKAGE'S SOURCE
//
// A source-level grep for `%s%s` passes the moment somebody writes the same thing a
// different way, and it cannot see the half of the fix that lives in the page <head>.
// Rendering the real handler through the real gate is the only version that fails for
// the reason the owner would fail it.
//
// AND WHY EACH CHECK IS ALSO RUN AGAINST THE MARKUP IT IS SUPPOSED TO REJECT
//
// A guard that only ever sees passing input proves nothing about what it would do with
// failing input. Every check below is therefore called twice: once on the real page
// (must pass) and once on a fixture of the exact markup this plug used to emit (must
// fail). If a later refactor weakens a check into something that cannot fail, the
// negative case is what says so.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"xf/controller/pkg/webui"
)

// gatedGet renders one of the plug's own surfaces as a signed-in member would see it.
// Remote-Email + Remote-Groups is exactly what the edge injects and what
// flowweb.AuthGate reads, so this is the real gate rather than a bypass of it.
func gatedGet(t *testing.T, a *app, h http.HandlerFunc, path string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: got HTTP %d, so the assertions below would be measuring a denial page", path, w.Code)
	}
	body := w.Body.String()
	if strings.TrimSpace(body) == "" {
		t.Fatalf("%s rendered nothing", path)
	}
	return body
}

// mainOf narrows a rendered page to the part this plug actually writes.
//
// The header nav and the injected feedback dock both render runs of adjacent controls
// (.ptabs > .btn, .fb-btn) and both are CORRECT — their containers own their gaps, they
// come from the shared layer, and no edit in this repository could change them. Checking
// the whole document would therefore fail for something this plug cannot fix, which is
// the fastest way to get a guard switched off.
func mainOf(t *testing.T, path, page string) string {
	t.Helper()
	start := strings.Index(page, "<main>")
	end := strings.Index(page, "</main>")
	if start < 0 || end < start {
		t.Fatalf("%s: no <main> in the rendered page, so there is nothing to check", path)
	}
	return page[start+len("<main>") : end]
}

// spacingContainers are the house wrappers that own the gap between controls. Every one
// of them takes its spacing from BaseCSS, which every page of every plug loads — that is
// the whole reason a plug-owned wrapper is not an acceptable substitute (its stylesheet
// is attached to a route, while the markup is attached to a function).
var spacingContainers = regexp.MustCompile(`<div class="(?:field-row|btn-group|action-bar)">.*?</div>`)

// adjacentButtons matches two controls that sit side by side with nothing between them
// but whitespace — the shape that renders edge to edge.
var adjacentButtons = regexp.MustCompile(`</(?:a|button)>\s*<(?:a|button)[^>]*class="[^"]*\bbtn\b`)

// unspacedButtonPairs returns the adjacent-control pairs left in html once every house
// spacing container has been removed. Inside one of those containers adjacency is
// CORRECT and expected — the gap comes from the container, not from the markup — so a
// check that merely looked for adjacency would fail on the fixed page too.
func unspacedButtonPairs(html string) []string {
	return adjacentButtons.FindAllString(spacingContainers.ReplaceAllString(html, ""), -1)
}

// tablesOutsideAWrap returns the table elements in html that are not opened directly
// inside a .lv-tablewrap. The wrap is what scrolls; without it the table simply widens
// its container.
func tablesOutsideAWrap(html string) int {
	n := 0
	for _, idx := range regexp.MustCompile(`<table[\s>]`).FindAllStringIndex(html, -1) {
		before := html[:idx[0]]
		if !strings.HasSuffix(before, `<div class="lv-tablewrap">`) {
			n++
		}
	}
	return n
}

// The reported defect, on the page it was reported against. /addon carries two pairs of
// buttons; before the fix both were emitted as `%s%s` inside a <p> and both rendered
// touching.
func TestNoTwoControlsRenderWithoutASpacingContainer(t *testing.T) {
	a := testApp()
	for _, tc := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{"/addon", a.serveAddon},
		{"/pair", a.servePair},
	} {
		body := mainOf(t, tc.path, gatedGet(t, a, tc.h, tc.path))
		if got := unspacedButtonPairs(body); len(got) > 0 {
			t.Errorf("%s renders %d control pair(s) with no container to space them: %q\n"+
				"Put them in webui.ButtonRow/ButtonGroup — the container owns the gap (STYLEGUIDE §3), "+
				"and two controls emitted adjacently have nothing between them at all.",
				tc.path, len(got), got)
		}
	}

	// The negative case: the exact markup /addon used to emit. If this stops failing,
	// the check above has stopped being able to see the bug it exists for.
	was := `<p style="margin:.6rem 0"><a class="btn primary" href="/addon/zmsync.koplugin.zip">` +
		`<i class="fa-solid fa-download" aria-hidden="true"></i> Download zmsync.koplugin.zip</a>` +
		`<a class="btn secondary" href="/pair"><i class="fa-solid fa-key" aria-hidden="true"></i> Pair a device</a></p>`
	if len(unspacedButtonPairs(was)) == 0 {
		t.Error("the touching-buttons check no longer detects the markup that was reported, " +
			"so it would pass whether or not the bug came back")
	}
}

// The other reported defect. A device row carries a statistics schema like
// page_stat_data(id_book,page,start_time,duration,total_pages), which is what set the
// width that escaped the card.
func TestEveryTableRendersInsideAnOverflowWrap(t *testing.T) {
	a := testApp()

	// A real paired device, so /pair renders the table rather than its empty state —
	// an empty list would make this test pass by having nothing to check.
	dev, _, tk := pairedDevice(t, a)
	dev.LastSyncAt, dev.KOReaderVersion = "2026-08-25T08:15:59Z", "v2026.04"
	dev.StatsSchema = "page_stat_data(id_book,page,start_time,duration,total_pages)"
	if err := a.devices.Put(context.Background(), tk, dev.ID, &dev); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{"/addon", a.serveAddon},
		{"/pair", a.servePair},
	} {
		body := mainOf(t, tc.path, gatedGet(t, a, tc.h, tc.path))
		if !strings.Contains(body, "<table") {
			t.Fatalf("%s renders no table at all, so this test is checking nothing", tc.path)
		}
		if n := tablesOutsideAWrap(body); n > 0 {
			t.Errorf("%s renders %d table(s) outside .lv-tablewrap: use webui.SimpleTable. "+
				"BaseCSS only gives a bare table an overflow container inside "+
				"@media(max-width:640px), so above that it grows out of its card.", tc.path, n)
		}
	}

	if tablesOutsideAWrap(`<div class="card"><table><thead><tr><th>Reader</th></tr></thead></table></div>`) == 0 {
		t.Error("the overflow check no longer detects an unwrapped table")
	}
}

// The half of the table fix that is not in the markup. .lv-tablewrap and the script that
// collapses an overflowing row to cards both arrive from webui.ListViewHead(); a page
// that emits the classes without loading the stylesheet has the same overflow it started
// with and markup that looks correct in a diff.
func TestThePageLoadsTheStylesheetThatMakesTheWrapScroll(t *testing.T) {
	a := testApp()
	body := gatedGet(t, a, a.servePair, "/pair")
	for _, want := range []string{webui.ListViewCSSPath, webui.ListViewJSPath} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never references %s, so .lv-tablewrap is an unstyled div "+
				"and the table overflows exactly as before", want)
		}
	}
}

// A class that matches no selector anywhere is indistinguishable, in a diff, from one
// that works. The td class="mono" this plug shipped with was such a class: the only
// .mono rule in the fleet is `.card-meta .cm-val.mono`, so outside a card-meta list the
// token tail and the statistics schema rendered in the body font — and the schema, being
// one long unbroken token, is what set the table's width.
func TestMachineValuesRenderAsCodeSoTheyCanWrap(t *testing.T) {
	a := testApp()
	dev, _, tk := pairedDevice(t, a)
	dev.StatsSchema = "page_stat_data(id_book,page,start_time,duration,total_pages)"
	if err := a.devices.Put(context.Background(), tk, dev.ID, &dev); err != nil {
		t.Fatal(err)
	}
	body := gatedGet(t, a, a.servePair, "/pair")

	if !strings.Contains(body, "<code>"+dev.StatsSchema+"</code>") {
		t.Error("the statistics schema is not in a <code>, which is the role the shared theme " +
			"dresses — and the rule that carries overflow-wrap:anywhere, without which one " +
			"unbroken token sets the width of the whole table")
	}
	if !strings.Contains(body, "<code>…"+dev.TokenTail+"</code>") {
		t.Errorf("the token tail is not in a <code>: %q", dev.TokenTail)
	}
	if strings.Contains(body, `class="mono"`) {
		t.Error(`class="mono" is back: it matches no selector outside .card-meta, so it styles nothing`)
	}
}

// tokenTail must not print a lone ellipsis standing in for nothing.
func TestAnAbsentTokenTailShowsNothingRatherThanAnEllipsis(t *testing.T) {
	if got := tokenTail(""); got != "" {
		t.Errorf("tokenTail(%q) = %q, want empty", "", got)
	}
	if got := tokenTail("  "); got != "" {
		t.Errorf("tokenTail of whitespace = %q, want empty", got)
	}
	if got := tokenTail("ld9k"); got != "…ld9k" {
		t.Errorf("tokenTail(%q) = %q", "ld9k", got)
	}
}
