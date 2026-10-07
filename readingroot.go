package main

// readingroot.go — who owns "/" and the styles the reading pages need.

import "net/http"

// serveReadingRoot puts the calendar on the front page and hands everything else to the
// generated CRUD surface that used to own "/".
//
// ServeMux's "/" is a catch-all, not an exact match, so whoever registers it answers every
// unmatched path. Before this, that was the scaffolded Book list — which meant the actual
// reading, the whole point of the plug, had no page at all while a placeholder entity had
// the front door. Swapping them without losing /book/new means delegating rather than
// replacing.
func (a *app) serveReadingRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if a.generated != nil {
			a.generated.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	a.serveReading(w, r)
}

// koReadingCSS is the calendar and the stat tiles. Quotes are the shared browser's.
//
// EVERY COLOUR IS A BARE var() OFF THE SHARED THEME, with no literal fallback. A hex
// fallback looks like defensive programming and is the opposite: it is a second, invisible
// palette that only appears when the theme fails to load, and it pins that half of the page
// to light mode. dynlint holds hardcoded_hex_colour at ZERO here for exactly that reason,
// and it caught fourteen of them in the first draft of this file.
//
// The accent ramp is the theme's own accent at four opacities via color-mix, so the grid
// stays coherent when the theme changes underneath it. Spacing is the shared --sp-* scale
// (STYLEGUIDE §3); the only hard numbers are the grid's cell size, which is what a
// year-of-days IS.
//
// Corners are the house radius TOKENS. These rules used var(--radius), which the theme
// never defined: an undefined var() computes to 0, so the stat tiles were square and even
// overrode the house's own tile rounding (owner feedback d9e112f4, "needs rounded (house)
// element").
const koReadingCSS = `<style>
/* A list that IS the page starts at the top of it. The shared component carries
   .listview{margin:1rem 0}, which is right when a list follows prose or a form and wrong
   when it is the first thing under the nav — there it just holds the search box away from
   the header. Scoped to :first-child so only the leading list loses the margin, and only
   on this plug's pages rather than every list in the mesh. */
main > .listview:first-child{margin-top:0}
.ko-tiles{display:flex;flex-wrap:wrap;gap:var(--sp-3);margin:0 0 var(--sp-4)}
.ko-tile{flex:1 1 7rem;background:var(--card);border:1px solid var(--border);
  border-radius:var(--r-card);padding:var(--sp-3)}
.ko-tile-v{display:block;font-size:1.6rem;line-height:1.1;font-weight:600}
.ko-tile-l{display:block;font-size:.8rem;color:var(--muted)}

.ko-calwrap{overflow-x:auto;padding-bottom:var(--sp-2)}
.ko-cal{display:inline-block;min-width:max-content}
.ko-cal-months{display:grid;grid-auto-flow:column;grid-auto-columns:13px;
  font-size:.7rem;color:var(--muted);margin-bottom:2px}
.ko-cal-month{white-space:nowrap;overflow:visible}
.ko-cal-grid{display:grid;grid-template-rows:repeat(7,13px);grid-auto-flow:column;
  grid-auto-columns:13px;gap:2px}
.ko-day{width:11px;height:11px;border-radius:2px;display:block;
  background:var(--border);position:relative}
a.ko-day:hover{outline:2px solid var(--accent);outline-offset:1px}
.ko-day-void{background:transparent}
.ko-l0{background:var(--border)}
.ko-l1{background:color-mix(in srgb,var(--accent) 25%,transparent)}
.ko-l2{background:color-mix(in srgb,var(--accent) 45%,transparent)}
.ko-l3{background:color-mix(in srgb,var(--accent) 70%,transparent)}
.ko-l4{background:var(--accent)}
.ko-day-marked::after{content:"";position:absolute;right:1px;top:1px;width:3px;height:3px;
  border-radius:50%;background:var(--card);box-shadow:0 0 0 1px var(--accent)}
.ko-legend{display:flex;align-items:center;gap:var(--sp-2);margin:var(--sp-2) 0;
  font-size:.75rem;flex-wrap:wrap}
.ko-legend-note{margin-left:var(--sp-3)}

/* The quote itself — its passage, the reader, the pager — is styled by the shared
   quote browser (controller/pkg/quotebrowser), which every quote view uses. */
.ko-grade{display:inline}

.ko-page{margin:var(--sp-3) 0 0;border:1px solid var(--border);border-radius:var(--r-card);
  padding:var(--sp-2) var(--sp-3);background:var(--card)}
.ko-page summary{cursor:pointer;color:var(--muted);font-size:.85rem}
.ko-page-text{margin-top:var(--sp-2);white-space:pre-wrap;line-height:1.55;max-width:66ch}
.ko-page-wait{margin:var(--sp-2) 0 0;font-size:.85rem}
</style>`
