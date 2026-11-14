package main

// yearcal.go — /year: the whole year in the month-calendar style (a box per day, a bar per book, a
// histogram of the hours), a year selector, the year's numbers, and the same picture as a PNG to
// keep or share (/year.png, drawn in yearimg.go). The Reader home stays the habit grid; the
// calendar's Year button comes here.

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// Copy, written by Opus.
const (
	yearTitle     = "Year"
	yearSave      = "Save as image"
	yearSaveTip   = "Download this year as a picture to share"
	yearMonthView = "Month"
	yearEmpty     = "Nothing read in %d."
)

type topBook struct {
	Title   string
	Seconds int
}

type yearStats struct {
	Year                                     int
	Days, Seconds, Pages, Quotes, Books, Streak int
	Top                                      []topBook
}

// buildYearStats is the year's numbers: days read, hours, distinct books, pages, quotes, the longest
// run of consecutive days, and the five books read longest.
func buildYearStats(idx *readingIndex, loc *time.Location, year int) yearStats {
	st := yearStats{Year: year}
	prefix := fmt.Sprintf("%04d-", year)
	secs := map[string]int{}
	titles := map[string]string{}
	var dates []string
	for date, d := range idx.Days {
		if !strings.HasPrefix(date, prefix) || d.Seconds == 0 {
			continue
		}
		dates = append(dates, date)
		st.Days++
		st.Seconds += d.Seconds
		st.Pages += d.Pages
		for _, b := range d.Books {
			secs[b.BookMD5] += b.Seconds
			titles[b.BookMD5] = b.Title
		}
	}
	st.Books = len(secs)
	for _, an := range idx.Highlights() {
		if readingDate(an.StartTime, loc)[:4] == prefix[:4] {
			st.Quotes++
		}
	}
	sort.Strings(dates)
	run, prev := 0, time.Time{}
	for _, ds := range dates {
		t, err := time.Parse("2006-01-02", ds)
		if err != nil {
			continue
		}
		if !prev.IsZero() && t.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		prev = t
		st.Streak = max(st.Streak, run)
	}
	for md5, s := range secs {
		st.Top = append(st.Top, topBook{Title: titles[md5], Seconds: s})
	}
	sort.Slice(st.Top, func(i, j int) bool {
		if st.Top[i].Seconds != st.Top[j].Seconds {
			return st.Top[i].Seconds > st.Top[j].Seconds
		}
		return st.Top[i].Title < st.Top[j].Title
	})
	if len(st.Top) > 5 {
		st.Top = st.Top[:5]
	}
	return st
}

// parseYear is the ?y= year: a sane number, else the latest year that has reading, else this one.
func parseYear(s string, idx *readingIndex, loc *time.Location, now time.Time) int {
	if y, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && y >= 1900 && y <= 2200 {
		return y
	}
	if ys := idx.Years(); len(ys) > 0 {
		return ys[0]
	}
	return now.In(loc).Year()
}

func yearSwitch(years []int, active int) template.HTML {
	var b strings.Builder
	b.WriteString(`<div class="field-row" role="group" aria-label="Year">`)
	for _, y := range years {
		kind := "secondary"
		if y == active {
			kind = "primary"
		}
		href := "/year?y=" + strconv.Itoa(y)
		b.WriteString(string(webui.IconTextButton("", strconv.Itoa(y), webui.BtnOpts{Kind: kind, Size: "sm", Href: href,
			Attrs: ` hx-get="` + href + `" hx-target="#ko-year" hx-swap="outerHTML" hx-select="#ko-year" hx-push-url="true"`})))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// yearYears is the selector's years: every year with reading, the one shown, and this one, newest first.
func yearYears(idx *readingIndex, active int, now time.Time, loc *time.Location) []int {
	seen := map[int]bool{active: true, now.In(loc).Year(): true}
	for _, y := range idx.Years() {
		seen[y] = true
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

func renderYear(idx *readingIndex, loc *time.Location, year int, now time.Time) template.HTML {
	st := buildYearStats(idx, loc, year)
	var b strings.Builder
	b.WriteString(`<div id="ko-year"><div class="ko-yearbar">`)
	b.WriteString(string(yearSwitch(yearYears(idx, year, now, loc), year)))
	b.WriteString(string(webui.IconTextButton("fa-solid fa-image", yearSave, webui.BtnOpts{Kind: "secondary", Size: "sm",
		Href:  fmt.Sprintf("/year.png?y=%d&dl=1", year),
		Attrs: ` hx-boost="false" download="reading-` + strconv.Itoa(year) + `.png" data-tip="` + template.HTMLEscapeString(yearSaveTip) + `"`})))
	b.WriteString(`</div>`)
	if st.Days == 0 {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, fmt.Sprintf(yearEmpty, year))
	} else {
		b.WriteString(string(webui.StatGrid([]webui.Stat{
			{Label: "Days read", Value: fmt.Sprint(st.Days)},
			{Label: "Hours", Value: fmt.Sprintf("%.0f", float64(st.Seconds)/3600)},
			{Label: "Books", Value: fmt.Sprint(st.Books)},
			{Label: "Pages", Value: fmt.Sprint(st.Pages)},
			{Label: "Quotes", Value: fmt.Sprint(st.Quotes)},
			{Label: "Best streak", Value: fmt.Sprint(st.Streak)},
		})))
	}
	b.WriteString(`<div class="ko-ygrid">`)
	for m := 1; m <= 12; m++ {
		mc := buildMonthCal(idx, loc, time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc), now)
		fmt.Fprintf(&b, `<section class="ko-ymonth"><h3><a href="/calendar?m=%s" title="%s">%s</a></h3>%s</section>`,
			mc.Month, yearMonthView, time.Month(m).String(), renderMonthGrid(mc))
	}
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

// yearCSS lays the twelve months out four to a row, as the picture does, so a year can be seen at once.
// The month cell keeps its style (box, bars, histogram); only its size changes: the lanes and the
// histogram row are thin, and a bar's label is dropped (its book is the tooltip), since at this size
// the label could not be read anyway. Below 64rem it is two a row, and one on a phone.
const yearCSS = `<style>
.ko-yearbar{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:var(--sp-2);margin:0 0 var(--sp-3)}
.ko-ygrid{display:grid;grid-template-columns:minmax(0,1fr);gap:var(--sp-3) var(--sp-4);margin-top:var(--sp-2)}
@media (min-width: 36rem){.ko-ygrid{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media (min-width: 64rem){.ko-ygrid{grid-template-columns:repeat(4,minmax(0,1fr))}}
.ko-ymonth{min-width:0;--ko-row:.3rem}
.ko-ymonth h3{margin:0 0 var(--sp-1);font-size:.9rem}
.ko-ymonth h3 a{color:var(--text-hi);text-decoration:none}
.ko-ymonth .ko-mhead{font-size:.65rem}
.ko-ymonth .ko-week{row-gap:1px;padding-bottom:0;grid-template-rows:auto repeat(4,var(--ko-row)) calc(var(--ko-row)*2)}
.ko-ymonth .ko-dn{font-size:.65rem;line-height:1.2;padding:var(--sp-0) var(--sp-0) 0}
.ko-ymonth .ko-bar{font-size:0;line-height:0;padding:0;border-left-width:2px}
.ko-ymonth .ko-ticks{height:auto;margin:0 1px 1px}
</style>`

func (a *app) serveYear(w http.ResponseWriter, r *http.Request) {
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
	now := time.Now()
	year := parseYear(r.URL.Query().Get("y"), idx, loc, now)
	p := a.page("/year")
	p.Width = flowweb.WidthWide
	p.CSS += koCalCSS() + yearCSS
	p.StartFor(w, r, yearTitle)
	fmt.Fprint(w, renderYear(idx, loc, year, now))
	p.End(w)
}

// serveYearImage is the year as a PNG; dl=1 asks the browser to save it rather than show it.
func (a *app) serveYearImage(w http.ResponseWriter, r *http.Request) {
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
	now := time.Now()
	year := parseYear(r.URL.Query().Get("y"), idx, loc, now)
	body, err := yearPNG(idx, loc, year, now)
	if err != nil {
		http.Error(w, "could not draw the picture", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	disp := "inline"
	if r.URL.Query().Get("dl") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="reading-%d.png"`, disp, year))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
