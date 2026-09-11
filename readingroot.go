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

// koReadingCSS is the calendar, the stat tiles and the quote blocks.
//
// Every colour is a var() off the shared theme with a literal fallback, so the grid follows
// light/dark with everything else rather than being a light-mode island — the accent ramp
// is the theme's own accent at four opacities, which is why it stays coherent when the
// theme changes underneath it. Spacing comes off the shared --sp-* scale (STYLEGUIDE §3);
// the only hard numbers are the grid's cell size, which is what a year-of-days IS.
const koReadingCSS = `<style>
.ko-tiles{display:flex;flex-wrap:wrap;gap:var(--sp-3,.75rem);margin:0 0 var(--sp-4,1rem)}
.ko-tile{flex:1 1 7rem;background:var(--card,#fff);border:1px solid var(--border,#e5e7eb);
  border-radius:var(--radius,8px);padding:var(--sp-3,.75rem)}
.ko-tile-v{display:block;font-size:1.6rem;line-height:1.1;font-weight:600}
.ko-tile-l{display:block;font-size:.8rem;color:var(--muted,#6b7280)}

.ko-calwrap{overflow-x:auto;padding-bottom:var(--sp-2,.5rem)}
.ko-cal{display:inline-block;min-width:max-content}
.ko-cal-months{display:grid;grid-auto-flow:column;grid-auto-columns:13px;
  font-size:.7rem;color:var(--muted,#6b7280);margin-bottom:2px}
.ko-cal-month{grid-row:1;white-space:nowrap}
.ko-cal-grid{display:grid;grid-template-rows:repeat(7,13px);grid-auto-flow:column;
  grid-auto-columns:13px;gap:2px}
.ko-day{width:11px;height:11px;border-radius:2px;display:block;
  background:var(--border,#e5e7eb);position:relative}
a.ko-day:hover{outline:2px solid var(--accent,#2563eb);outline-offset:1px}
.ko-day-void{background:transparent}
.ko-l0{background:var(--border,#e5e7eb)}
.ko-l1{background:color-mix(in srgb,var(--accent,#2563eb) 25%,transparent)}
.ko-l2{background:color-mix(in srgb,var(--accent,#2563eb) 45%,transparent)}
.ko-l3{background:color-mix(in srgb,var(--accent,#2563eb) 70%,transparent)}
.ko-l4{background:var(--accent,#2563eb)}
.ko-day-marked::after{content:"";position:absolute;right:1px;top:1px;width:3px;height:3px;
  border-radius:50%;background:var(--card,#fff);box-shadow:0 0 0 1px var(--accent,#2563eb)}
.ko-legend{display:flex;align-items:center;gap:var(--sp-2,.5rem);margin:var(--sp-2,.5rem) 0;
  font-size:.75rem;flex-wrap:wrap}
.ko-legend-note{margin-left:var(--sp-3,.75rem)}

.ko-quote{margin:0 0 var(--sp-3,.75rem);padding:var(--sp-3,.75rem);
  border-left:3px solid var(--accent,#2563eb);background:var(--bg-soft,rgba(0,0,0,.03));
  border-radius:0 var(--radius,8px) var(--radius,8px) 0}
.ko-bookmark{border-left-color:var(--muted,#6b7280)}
.ko-quote-text{margin:0 0 var(--sp-2,.5rem);font-size:1.05rem;line-height:1.5}
.ko-quote-note{margin:0 0 var(--sp-2,.5rem);font-style:italic;color:var(--muted,#6b7280)}
.ko-quote footer{font-size:.8rem}
.ko-quote-cell{display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;
  overflow:hidden;max-width:60ch}
.ko-review .ko-quote{font-size:1.1rem}
.ko-grade{display:inline}
</style>`
