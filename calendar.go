package main

// calendar.go — the month calendar: one bar per book across the days it was read, and a
// small histogram of the day's reading at the foot of each cell. The same view KOReader's
// statistics screen draws, built from the sessions the device already syncs.
//
// Everything is positioned by CLASS (column start, span, lane), never by inline style, so
// the page themes and dynlint stays at zero. Each week is its own grid: a row of day
// numbers, one row per lane of bars, and a row of ticks. A page that is one server render
// has nothing to load in after the first paint, so nothing shifts.

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/webui"
	"xf/controller/pkg/xats"
)

const (
	capCalendarGet = "reader.calendar.get"
	maxLanes       = 4 // book rows in a cell; the cell is the same size whatever a week holds
	calTones       = 5
	tickBuckets    = 24 // one per hour, midnight to midnight
)

// calBar is one book's run of consecutive reading days inside one week row.
type calBar struct {
	MD5, Title string
	Col, Span  int // 1-based start column, days covered
	Lane       int // 1-based
	Tone       int
	FirstDay   string
	Quotes     bool
}

type calDay struct {
	Date    string
	Num     int
	InMonth bool
	Today   bool
	Minutes int
	Ticks   [tickBuckets]int // 0 none .. 4 most
}

type calWeek struct {
	Days  [7]calDay
	Bars  []calBar
	Lanes int
}

type monthCal struct {
	Month                  string // 2026-11
	Prev, Next, PrevY, NextY string
	Weeks                  []calWeek
	Books                  int
	Minutes                int
}

func parseMonth(s string, loc *time.Location, now time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01", strings.TrimSpace(s), loc); err == nil {
		return t
	}
	n := now.In(loc)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
}

// buildMonthCal is pure: the index, the zone and a month in, the grid out.
func buildMonthCal(idx *readingIndex, loc *time.Location, month, now time.Time) monthCal {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, loc)
	start := first
	for start.Weekday() != time.Monday {
		start = start.AddDate(0, 0, -1)
	}
	last := first.AddDate(0, 1, -1)
	today := now.In(loc).Format("2006-01-02")
	out := monthCal{
		Month: first.Format("2006-01"),
		Prev:  first.AddDate(0, -1, 0).Format("2006-01"), Next: first.AddDate(0, 1, 0).Format("2006-01"),
		PrevY: first.AddDate(-1, 0, 0).Format("2006-01"), NextY: first.AddDate(1, 0, 0).Format("2006-01"),
	}

	// One tone per book, by order of first appearance in the month, so neighbours differ.
	tone := map[string]int{}
	quotes := map[string]bool{}
	for _, an := range idx.Highlights() {
		quotes[an.BookMD5] = true
	}
	books := map[string]bool{}

	for ws := start; !ws.After(last); ws = ws.AddDate(0, 0, 7) {
		var w calWeek
		type run struct {
			title string
			col   int
			secs  int
		}
		perBook := map[string][]int{} // md5 -> columns read (0-based)
		titles := map[string]string{}
		secs := map[string]int{}
		for c := 0; c < 7; c++ {
			d := ws.AddDate(0, 0, c)
			date := d.Format("2006-01-02")
			cd := calDay{Date: date, Num: d.Day(), InMonth: d.Month() == first.Month(), Today: date == today}
			if ds := idx.Days[date]; ds != nil && cd.InMonth {
				cd.Minutes = ds.Minutes()
				var b [tickBuckets]int
				for h, s := range ds.HourSeconds {
					b[h%tickBuckets] += s
				}
				for i, s := range b {
					switch {
					case s <= 0:
					case s < 300:
						cd.Ticks[i] = 1
					case s < 900:
						cd.Ticks[i] = 2
					case s < 1800:
						cd.Ticks[i] = 3
					default:
						cd.Ticks[i] = 4
					}
				}
				for _, bd := range ds.Books {
					perBook[bd.BookMD5] = append(perBook[bd.BookMD5], c)
					titles[bd.BookMD5] = bd.Title
					secs[bd.BookMD5] += bd.Seconds
					books[bd.BookMD5] = true
					if _, ok := tone[bd.BookMD5]; !ok {
						tone[bd.BookMD5] = len(tone) % calTones
					}
				}
				out.Minutes += ds.Minutes()
			}
			w.Days[c] = cd
		}
		// Runs of consecutive columns per book.
		var bars []calBar
		for md5, cols := range perBook {
			sort.Ints(cols)
			i := 0
			for i < len(cols) {
				j := i
				for j+1 < len(cols) && cols[j+1] == cols[j]+1 {
					j++
				}
				bars = append(bars, calBar{MD5: md5, Title: displayTitle(titles[md5]), Col: cols[i] + 1,
					Span: cols[j] - cols[i] + 1, Tone: tone[md5], FirstDay: w.Days[cols[i]].Date, Quotes: quotes[md5]})
				i = j + 1
			}
		}
		sort.Slice(bars, func(i, j int) bool {
			if bars[i].Col != bars[j].Col {
				return bars[i].Col < bars[j].Col
			}
			if secs[bars[i].MD5] != secs[bars[j].MD5] {
				return secs[bars[i].MD5] > secs[bars[j].MD5]
			}
			return bars[i].MD5 < bars[j].MD5
		})
		var laneEnd [maxLanes]int
		for _, b := range bars {
			placed := false
			for l := 0; l < maxLanes; l++ {
				if laneEnd[l] < b.Col {
					b.Lane = l + 1
					laneEnd[l] = b.Col + b.Span - 1
					if b.Lane > w.Lanes {
						w.Lanes = b.Lane
					}
					placed = true
					break
				}
			}
			if placed {
				w.Bars = append(w.Bars, b)
			}
		}
		out.Weeks = append(out.Weeks, w)
	}
	out.Books = len(books)
	return out
}

// Copy, written by Opus.
const (
	calTitle      = "Calendar"
	calMonthView  = "Month"
	calYearView   = "Year"
	calPrevMonth  = "Previous month"
	calNextMonth  = "Next month"
	calPrevYear   = "Previous year"
	calNextYear   = "Next year"
	calEmptyMonth = "Nothing read in this month."
	calSummary    = "%d min across %d books"
	calSummaryOne = "%d min in 1 book"
)

var calDayNames = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func renderMonthCal(c monthCal) template.HTML {
	var b strings.Builder
	nav := func(icon, label, month string) {
		b.WriteString(string(webui.IconTextButton(icon, "", webui.BtnOpts{Size: "sm", Href: "/calendar?m=" + month, Title: label,
			Attrs: `aria-label="` + template.HTMLEscapeString(label) + `"`})))
	}
	b.WriteString(`<div class="ko-calnav">`)
	nav("fa-solid fa-angles-left", calPrevYear, c.PrevY)
	nav("fa-solid fa-angle-left", calPrevMonth, c.Prev)
	fmt.Fprintf(&b, `<span class="ko-calmonth">%s</span>`, c.Month)
	nav("fa-solid fa-angle-right", calNextMonth, c.Next)
	nav("fa-solid fa-angles-right", calNextYear, c.NextY)
	b.WriteString(string(webui.IconTextButton("fa-solid fa-table-cells", calYearView, webui.BtnOpts{Size: "sm", Href: "/"})))
	b.WriteString(`</div>`)
	if c.Books == 0 {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, calEmptyMonth)
	} else if c.Books == 1 {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, fmt.Sprintf(calSummaryOne, c.Minutes))
	} else {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, fmt.Sprintf(calSummary, c.Minutes, c.Books))
	}
	b.WriteString(`<div class="ko-mcal"><div class="ko-mhead">`)
	for _, n := range calDayNames {
		fmt.Fprintf(&b, `<span>%s</span>`, n)
	}
	b.WriteString(`</div>`)
	for _, w := range c.Weeks {
		tickRow := maxLanes + 2 // the foot of the cell, below the book rows, whether or not they are used
		fmt.Fprintf(&b, `<div class="ko-week">`)
		for i, d := range w.Days {
			cls := "ko-dn ko-a" + fmt.Sprint(i+1)
			if !d.InMonth {
				cls += " ko-out"
			}
			if d.Today {
				cls += " ko-today"
			}
			if d.Minutes > 0 {
				fmt.Fprintf(&b, `<a class="%s ko-r1" href="/day?d=%s" aria-label="%s: %d min">%d</a>`, cls, d.Date, d.Date, d.Minutes, d.Num)
			} else {
				fmt.Fprintf(&b, `<span class="%s ko-r1">%d</span>`, cls, d.Num)
			}
		}
		for _, bar := range w.Bars {
			href := "/day?d=" + bar.FirstDay
			if bar.Quotes {
				href = "/quote?book=" + url.QueryEscape(bar.MD5)
			}
			fmt.Fprintf(&b, `<a class="ko-bar ko-a%d ko-n%d ko-r%d ko-t%d" href="%s" aria-label="%s">%s</a>`,
				bar.Col, bar.Span, bar.Lane+1, bar.Tone, href, template.HTMLEscapeString(bar.Title), webui.Text(bar.Title))
		}
		for i, d := range w.Days {
			if d.Minutes == 0 {
				continue
			}
			fmt.Fprintf(&b, `<a class="ko-ticks ko-a%d ko-r%d" href="/day?d=%s" aria-label="%s">`, i+1, tickRow, d.Date, d.Date)
			for _, t := range d.Ticks {
				fmt.Fprintf(&b, `<i class="ko-k%d"></i>`, t)
			}
			b.WriteString(`</a>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// koCalCSS: row classes r1..r8, column starts a1..a7, spans n1..n7, tones t0..t4.
func koCalCSS() string {
	var b strings.Builder
	b.WriteString(`<style>
.ko-calnav{display:flex;align-items:center;gap:var(--sp-2);margin:0 0 var(--sp-2)}
.ko-calmonth{flex:1;text-align:center;font-weight:600}
.ko-mcal{border:1px solid var(--border);border-radius:var(--r-card);background:var(--card);overflow:hidden}
.ko-mhead,.ko-week{display:grid;grid-template-columns:repeat(7,minmax(0,1fr))}
.ko-mhead{background:var(--th-bg);font-size:.75rem;color:var(--muted);text-align:center;padding:var(--sp-1) 0}
.ko-mcal{--ko-row:calc(.72rem*1.5)}
.ko-week{border-top:1px solid var(--border);row-gap:2px;padding-bottom:var(--sp-1);grid-template-rows:auto repeat(5,var(--ko-row))}
.ko-dn{padding:var(--sp-1) var(--sp-1) 0;font-size:.8rem;color:var(--text)}
.ko-out{color:var(--faint)}
.ko-today{font-weight:700;color:var(--accent)}
a.ko-dn{text-decoration:none}
.ko-bar{display:block;min-width:0;margin:0 1px;padding:0 var(--sp-1);font-size:.72rem;line-height:1.5;
  white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--text-hi);text-decoration:none;
  border-radius:var(--r-chip,4px);border-left:3px solid var(--bar)}
.ko-ticks{display:grid;grid-template-columns:repeat(24,minmax(0,1fr));align-items:end;height:var(--ko-row);margin:0 1px}
.ko-ticks i{display:block;background:var(--muted);border-radius:1px}
.ko-k0{height:1px;opacity:.3}.ko-k1{height:25%}.ko-k2{height:50%}.ko-k3{height:75%}.ko-k4{height:100%}
.ko-t0{--bar:var(--accent-fill);background:color-mix(in srgb,var(--accent-fill) 35%,var(--card))}
.ko-t1{--bar:var(--done);background:color-mix(in srgb,var(--done) 35%,var(--card))}
.ko-t2{--bar:var(--special);background:color-mix(in srgb,var(--special) 35%,var(--card))}
.ko-t3{--bar:var(--warn);background:color-mix(in srgb,var(--warn) 35%,var(--card))}
.ko-t4{--bar:var(--highlight);background:color-mix(in srgb,var(--highlight) 35%,var(--card))}
`)
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&b, ".ko-a%d{grid-column-start:%d}.ko-n%d{grid-column-end:span %d}\n", i, i, i, i)
	}
	for i := 1; i <= maxLanes+2; i++ {
		fmt.Fprintf(&b, ".ko-r%d{grid-row:%d}", i, i)
	}
	b.WriteString("\n</style>")
	return b.String()
}

func (a *app) serveCalendar(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	loc := a.deviceLoc(r.Context(), tk)
	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return
	}
	month := parseMonth(r.URL.Query().Get("m"), loc, time.Now())
	p := a.page("/calendar")
	p.CSS += koCalCSS()
	p.StartFor(w, r, calTitle)
	fmt.Fprint(w, renderMonthCal(buildMonthCal(idx, loc, month, time.Now())))
	p.End(w)
}

func calendarCapability() xats.Capability {
	return xats.Capability{
		Name: capCalendarGet, Command: capCalendarGet, Title: "Reading Calendar",
		Description: "One month of reading: for each week the books read as runs of days, and for each day the minutes and a histogram of when.",
		Icon:        "fa-solid fa-calendar-days", RequiredScopes: []string{scopeUser}, ExposeToMCP: true,
		Security:    []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}},
		Annotations: map[string]any{"readOnlyHint": true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"month": map[string]any{"type": "string", "description": "YYYY-MM; defaults to this month"}}},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"month": map[string]any{"type": "string"},
			"prev":  map[string]any{"type": "string"}, "next": map[string]any{"type": "string"},
			"minutes": map[string]any{"type": "integer"}, "books": map[string]any{"type": "integer"},
			"weeks": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"days": map[string]any{"type": "array", "description": "seven days, Monday first: date, minutes, ticks (24 hourly buckets from midnight, 0-4)"},
				"bars": map[string]any{"type": "array", "description": "book, title, start (1-7), span, lane, tone (0-4)"},
			}}},
		}},
	}
}

func (a *app) handleCalendarGet(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return map[string]any{"error": "could not read your reading", "body": "could not read your reading"}
	}
	loc := a.deviceLoc(ctx, tk)
	month, _ := meta["month"].(string)
	now := time.Now()
	c := buildMonthCal(idx, loc, parseMonth(month, loc, now), now)
	weeks := make([]map[string]any, 0, len(c.Weeks))
	for _, w := range c.Weeks {
		days := make([]map[string]any, 0, 7)
		for _, d := range w.Days {
			days = append(days, map[string]any{"date": d.Date, "in_month": d.InMonth, "minutes": d.Minutes, "ticks": d.Ticks[:]})
		}
		bars := make([]map[string]any, 0, len(w.Bars))
		for _, br := range w.Bars {
			bars = append(bars, map[string]any{"book": br.MD5, "title": br.Title, "start": br.Col, "span": br.Span, "lane": br.Lane, "tone": br.Tone})
		}
		weeks = append(weeks, map[string]any{"days": days, "bars": bars})
	}
	return map[string]any{"month": c.Month, "prev": c.Prev, "next": c.Next, "minutes": c.Minutes, "books": c.Books, "weeks": weeks}
}
