package main

// stats.go — /stats: one year's numbers in words and bars (days, hours, books, pages, best streak,
// busiest month, the books read longest, reading by weekday, by time of day and by month), under the
// same year selector as /year, with the same page as a PNG to keep (/stats.png, drawn here in the
// yearimg.go canvas). Owner, plan dab02a73.
//
// "Books read" is books with reading time in the year: KOReader's rows do not say a book was finished
// (pastlist.go), so a count of finished books would be a guess.

import (
	"bytes"
	"fmt"
	"html/template"
	"image/png"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"xf/controller/pkg/webui"
)

// Copy, written by Opus.
const (
	statsTitle            = "Stats"
	statsSave             = "Save as image"
	statsSaveTip          = "Save this year's stats as a picture"
	statsEmpty            = "Nothing read in %d."
	statsDaysRead         = "Days read"
	statsHours            = "Hours"
	statsBooksRead        = "Books read"
	statsPages            = "Pages"
	statsQuotes           = "Quotes"
	statsBestStreak       = "Best streak"
	statsStreakNote       = "From %s to %s"
	statsBusiestMonth     = "Busiest month"
	statsBusiestMonthNote = "%d hours"
	statsAvgDay           = "Daily average"
	statsAvgDayValue      = "%d min"
	statsMostReadHead     = "Books you read most"
	statsWeekdayHead      = "By day of the week"
	statsHourHead         = "By time of day"
	statsMonthHead        = "By month"
	statsMostReadNone     = "No books read this year."
	statsFavouriteDay     = "You read most on %ss."
	statsFavouriteHour    = "Your best hour is %s."
	statsPNGTitle         = "Reading stats %d"
)

var statsWeekdays = [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// yearDetail is yearStats and the shapes the Stats page adds.
type yearDetail struct {
	yearStats
	StreakFrom, StreakTo string // dates, "2 Jan"
	Month                [12]int
	Weekday              [7]int // Monday first, seconds
	Hour                 [24]int
	BusiestMonth         int // 0 when nothing was read
}

func shortDate(ds string) string {
	t, err := time.Parse("2006-01-02", ds)
	if err != nil {
		return ds
	}
	return t.Format("2 Jan")
}

func buildYearDetail(idx *readingIndex, loc *time.Location, year int) yearDetail {
	d := yearDetail{yearStats: buildYearStats(idx, loc, year)}
	prefix := fmt.Sprintf("%04d-", year)
	var dates []string
	for date, day := range idx.Days {
		if !strings.HasPrefix(date, prefix) || day.Seconds == 0 {
			continue
		}
		dates = append(dates, date)
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		d.Month[t.Month()-1] += day.Seconds
		d.Weekday[(int(t.Weekday())+6)%7] += day.Seconds
		for h, s := range day.HourSeconds {
			d.Hour[h] += s
		}
	}
	best := 0
	for m, s := range d.Month {
		if s > best {
			best, d.BusiestMonth = s, m+1
		}
	}
	// The streak's dates: the same walk buildYearStats does, keeping where the longest run began.
	sort.Strings(dates)
	run, start, prev := 0, "", time.Time{}
	for _, ds := range dates {
		t, err := time.Parse("2006-01-02", ds)
		if err != nil {
			continue
		}
		if !prev.IsZero() && t.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run, start = 1, ds
		}
		prev = t
		if run == d.Streak && d.StreakTo == "" {
			d.StreakFrom, d.StreakTo = shortDate(start), shortDate(ds)
		}
	}
	return d
}

func argMax(v []int) (int, int) {
	at, best := 0, 0
	for i, s := range v {
		if s > best {
			at, best = i, s
		}
	}
	return at, best
}

func hourLabel(h int) string {
	switch {
	case h == 0:
		return "12 am"
	case h < 12:
		return fmt.Sprintf("%d am", h)
	case h == 12:
		return "12 pm"
	}
	return fmt.Sprintf("%d pm", h-12)
}

func statsBars(head string, labels []string, vals []int, unit func(int) string) string {
	_, top := argMax(vals)
	var b strings.Builder
	fmt.Fprintf(&b, `<section class="ko-sect"><h3>%s</h3><div class="ko-bars">`, template.HTMLEscapeString(head))
	for i, v := range vals {
		pct := 0
		if top > 0 {
			pct = v * 100 / top
		}
		fmt.Fprintf(&b, `<div class="ko-brow"><span class="ko-blab">%s</span><span class="ko-btrack"><i style="width:%d%%"></i></span><span class="ko-bval muted">%s</span></div>`,
			template.HTMLEscapeString(labels[i]), pct, template.HTMLEscapeString(unit(v)))
	}
	b.WriteString(`</div></section>`)
	return b.String()
}

func hoursUnit(s int) string {
	if s == 0 {
		return ""
	}
	return humanDuration(s)
}

func monthNames() []string {
	out := make([]string, 12)
	for i := range out {
		out[i] = time.Month(i + 1).String()[:3]
	}
	return out
}

func hourLabels() []string {
	out := make([]string, 24)
	for h := range out {
		out[h] = hourLabel(h)
	}
	return out
}

func renderStats(idx *readingIndex, loc *time.Location, year int, now time.Time) template.HTML {
	d := buildYearDetail(idx, loc, year)
	var b strings.Builder
	b.WriteString(`<div id="ko-stats"><div class="ko-yearbar">`)
	b.WriteString(string(yearSwitchAt("/stats", "ko-stats", yearYears(idx, year, now, loc), year)))
	b.WriteString(string(webui.IconTextButton("fa-solid fa-image", statsSave, webui.BtnOpts{Kind: "secondary", Size: "sm",
		Href:  fmt.Sprintf("/stats.png?y=%d&dl=1", year),
		Attrs: ` hx-boost="false" download="reading-stats-` + strconv.Itoa(year) + `.png" data-tip="` + template.HTMLEscapeString(statsSaveTip) + `"`})))
	b.WriteString(`</div>`)
	if d.Days == 0 {
		fmt.Fprintf(&b, `<p class="muted">%s</p></div>`, fmt.Sprintf(statsEmpty, year))
		return template.HTML(b.String())
	}
	streak := ""
	if d.StreakTo != "" {
		streak = fmt.Sprintf(statsStreakNote, d.StreakFrom, d.StreakTo)
	}
	busy := time.Month(d.BusiestMonth).String()
	b.WriteString(string(webui.StatGrid([]webui.Stat{
		{Label: statsDaysRead, Value: fmt.Sprint(d.Days)},
		{Label: statsHours, Value: fmt.Sprintf("%.0f", float64(d.Seconds)/3600)},
		{Label: statsBooksRead, Value: fmt.Sprint(d.Books)},
		{Label: statsPages, Value: fmt.Sprint(d.Pages)},
		{Label: statsQuotes, Value: fmt.Sprint(d.Quotes)},
		{Label: statsBestStreak, Value: fmt.Sprint(d.Streak), Hint: streak},
		{Label: statsBusiestMonth, Value: busy, Hint: fmt.Sprintf(statsBusiestMonthNote, (d.Month[d.BusiestMonth-1]+1800)/3600)},
		{Label: statsAvgDay, Value: fmt.Sprintf(statsAvgDayValue, d.Seconds/d.Days/60)},
	})))
	wd, _ := argMax(d.Weekday[:])
	hr, _ := argMax(d.Hour[:])
	fmt.Fprintf(&b, `<p>%s %s</p>`, template.HTMLEscapeString(fmt.Sprintf(statsFavouriteDay, statsWeekdays[wd])), template.HTMLEscapeString(fmt.Sprintf(statsFavouriteHour, hourLabel(hr))))
	fmt.Fprintf(&b, `<section class="ko-sect"><h3>%s</h3>`, statsMostReadHead)
	if len(d.Top) == 0 {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, statsMostReadNone)
	} else {
		b.WriteString(`<ol class="ko-top">`)
		for _, t := range d.Top {
			fmt.Fprintf(&b, `<li><span title="%[1]s">%[1]s</span><span class="muted">%[2]s</span></li>`, template.HTMLEscapeString(displayTitle(t.Title)), humanDuration(t.Seconds))
		}
		b.WriteString(`</ol>`)
	}
	b.WriteString(`</section>`)
	b.WriteString(statsBars(statsWeekdayHead, statsWeekdays[:], d.Weekday[:], hoursUnit))
	b.WriteString(statsBars(statsHourHead, hourLabels(), d.Hour[:], hoursUnit))
	b.WriteString(statsBars(statsMonthHead, monthNames(), d.Month[:], hoursUnit))
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

const statsCSS = `<style>
.ko-sect{margin-top:var(--sp-4)}
.ko-sect h3{margin:0 0 var(--sp-2);font-size:1rem}
.ko-top{margin:0;padding:0;list-style:none;display:grid;gap:var(--sp-1)}
.ko-top li{display:flex;justify-content:space-between;gap:var(--sp-3)}
.ko-top li span:first-child{min-width:0;overflow-wrap:anywhere}
.ko-bars{display:grid;gap:2px}
.ko-brow{display:grid;grid-template-columns:5.5rem minmax(0,1fr) 5rem;align-items:center;gap:var(--sp-2);font-size:.85rem}
.ko-btrack{height:.6rem;background:var(--card);border:1px solid var(--border);border-radius:var(--r-chip,4px);overflow:hidden}
.ko-btrack i{display:block;height:100%;background:var(--accent)}
.ko-bval{text-align:right}
</style>`

func (a *app) statsInputs(w http.ResponseWriter, r *http.Request) (idx *readingIndex, loc *time.Location, year int, now time.Time, ok bool) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return nil, nil, 0, now, false
	}
	tk := tenant(caller)
	loc = a.deviceLoc(r.Context(), tk)
	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return nil, nil, 0, now, false
	}
	now = time.Now()
	return idx, loc, parseYear(r.URL.Query().Get("y"), idx, loc, now), now, true
}

func (a *app) serveStats(w http.ResponseWriter, r *http.Request) {
	idx, loc, year, now, ok := a.statsInputs(w, r)
	if !ok {
		return
	}
	p := a.page("/stats")
	p.CSS += yearCSS + statsCSS
	p.StartFor(w, r, statsTitle)
	fmt.Fprint(w, renderStats(idx, loc, year, now))
	p.End(w)
}

func (a *app) serveStatsImage(w http.ResponseWriter, r *http.Request) {
	idx, loc, year, _, ok := a.statsInputs(w, r)
	if !ok {
		return
	}
	body, err := statsPNG(idx, loc, year)
	if err != nil {
		http.Error(w, "could not draw the picture", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	disp := "inline"
	if r.URL.Query().Get("dl") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="reading-stats-%d.png"`, disp, year))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// drawBars is a captioned run of horizontal bars, one row per value, and returns the y below it.
func drawBars(c *canvas, y int, head string, labels []string, vals []int) int {
	c.text(imgMargin, y, 2, head, imgMuted)
	y += 22
	_, top := argMax(vals)
	trackX, trackW := imgMargin+110, imgW-2*imgMargin-110-90
	for i, v := range vals {
		c.text(imgMargin, y+1, 1, labels[i], imgText)
		c.rect(trackX, y, trackW, 8, imgCard)
		if top > 0 && v > 0 {
			c.rect(trackX, y, max(1, trackW*v/top), 8, imgAccent)
		}
		if v > 0 {
			t := humanDuration(v)
			c.text(imgW-imgMargin-textW(t, 1), y+1, 1, t, imgMuted)
		}
		y += 12
	}
	return y + 22
}

// statsPNG draws the Stats page: the title, the numbers, the books read longest and the three bar groups.
func statsPNG(idx *readingIndex, loc *time.Location, year int) ([]byte, error) {
	d := buildYearDetail(idx, loc, year)
	hu := 36 + 35 + 14 + 2*(62+12) + 26 + 22 + len(d.Top)*18 + 3*22 + (7+24+12)*12 + 60
	c := newCanvas(imgW, hu)
	c.rect(0, 0, imgW, hu, imgBG)
	c.text(imgMargin, 36, 5, fmt.Sprintf(statsPNGTitle, year), imgText)
	if d.Days == 0 {
		c.text(imgMargin, 36+35+14, 2, fmt.Sprintf(statsEmpty, year), imgMuted)
	} else {
		busy := time.Month(d.BusiestMonth).String()
		tiles := [][2]string{
			{fmt.Sprint(d.Days), statsDaysRead}, {fmt.Sprintf("%.0f", float64(d.Seconds)/3600), statsHours},
			{fmt.Sprint(d.Books), statsBooksRead}, {fmt.Sprint(d.Pages), statsPages},
			{fmt.Sprint(d.Quotes), statsQuotes}, {fmt.Sprint(d.Streak), statsBestStreak},
			{busy, statsBusiestMonth}, {fmt.Sprintf(statsAvgDayValue, d.Seconds/d.Days/60), statsAvgDay},
		}
		tw := (imgW - 2*imgMargin - 3*12) / 4
		for i, t := range tiles {
			x, y := imgMargin+(i%4)*(tw+12), 36+35+14+(i/4)*(62+12)
			c.rect(x, y, tw, 62, imgCard)
			c.stroke(x, y, tw, 62, imgBorder)
			// A tile shows its words whole: the number steps down a size before it would be cut.
			vs := 4
			for vs > 2 && textW(t[0], vs) > tw-20 {
				vs--
			}
			c.text(x+10, y+8, vs, t[0], imgText)
			c.text(x+10, y+42, 2, t[1], imgMuted)
		}
		y := 36 + 35 + 14 + 2*(62+12) + 26
		c.text(imgMargin, y, 2, statsMostReadHead, imgMuted)
		for i, b := range d.Top {
			ly := y + 22 + i*18
			c.text(imgMargin, ly, 2, fmt.Sprintf("%d. %s", i+1, fit(displayTitle(b.Title), (imgW-2*imgMargin)/12-14)), imgText)
			t := humanDuration(b.Seconds)
			c.text(imgW-imgMargin-textW(t, 2), ly, 2, t, imgMuted)
		}
		y += 22 + len(d.Top)*18 + 22
		y = drawBars(c, y, statsWeekdayHead, statsWeekdays[:], d.Weekday[:])
		y = drawBars(c, y, statsHourHead, hourLabels(), d.Hour[:])
		drawBars(c, y, statsMonthHead, monthNames(), d.Month[:])
	}
	c.img.Palette = c.pal
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
