package main

// readingweb.go — the pages that show what the device sent.
//
// Four surfaces, and each answers a different question:
//
//	/          WHEN did I read — a year of days, coloured by how long
//	/day       WHAT did I read on this day, and what did I mark
//	/quotes    every highlight, searchable (the shared quote browser, quotebrowse.go)
//	/review    the spaced-repetition queue
//
// The calendar is a year grid rather than a month view on purpose: reading is a habit, and
// a habit is a shape you only see at a year's scale. A month shows you twenty rectangles
// and tells you nothing about whether you are keeping it up.

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xf/controller/pkg/quotebrowser"
	"xf/controller/pkg/webui"
)

// serveReading is the calendar.
func (a *app) serveReading(w http.ResponseWriter, r *http.Request) {
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

	years := idx.Years()
	year := time.Now().In(loc).Year()
	if v, e := strconv.Atoi(r.URL.Query().Get("year")); e == nil && v > 1900 {
		year = v
	} else if len(years) > 0 {
		// Default to the most recent year that HAS reading, not to this one — landing on
		// an empty grid in January would suggest the sync had failed.
		found := false
		for _, y := range years {
			if y == year {
				found = true
				break
			}
		}
		if !found {
			year = years[0]
		}
	}

	p := a.page("/")
	p.StartFor(w, r, "Reading")

	if idx.TotalRead == 0 {
		fmt.Fprint(w, `<div class="card"><h3>Nothing here yet</h3>`)
		fmt.Fprint(w, `<p class="muted">No reading has arrived. Pair a reader on the <a href="/pair">Devices</a> page, then sync it from <strong>☰ → Tools → Zeitgeist reading sync</strong>.</p></div>`)
		p.End(w)
		return
	}

	fmt.Fprint(w, a.readingTotals(idx, loc))
	fmt.Fprint(w, yearNav(years, year))
	fmt.Fprint(w, calendarGrid(idx, year, loc))
	p.End(w)
}

// readingTotals is the headline strip: the numbers that say the pipeline works.
func (a *app) readingTotals(idx *readingIndex, loc *time.Location) template.HTML {
	var seconds, pages int
	for _, d := range idx.Days {
		seconds += d.Seconds
		pages += d.Pages
	}
	highlights := len(idx.Highlights())
	streak := currentStreak(idx, loc)

	tiles := []struct{ label, value string }{
		{"Days read", fmt.Sprintf("%d", len(idx.Dates))},
		{"Hours", fmt.Sprintf("%.0f", float64(seconds)/3600)},
		{"Pages", fmt.Sprintf("%d", pages)},
		{"Highlights", fmt.Sprintf("%d", highlights)},
		{"Current streak", fmt.Sprintf("%d", streak)},
	}
	var b strings.Builder
	b.WriteString(`<div class="ko-tiles">`)
	for _, t := range tiles {
		fmt.Fprintf(&b, `<div class="ko-tile"><span class="ko-tile-v">%s</span><span class="ko-tile-l">%s</span></div>`,
			webui.Text(t.value), webui.Text(t.label))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// currentStreak counts consecutive days ending today or yesterday. Yesterday counts
// because a streak should not appear broken at 09:00 before you have read that day.
func currentStreak(idx *readingIndex, loc *time.Location) int {
	if len(idx.Dates) == 0 {
		return 0
	}
	// Anchored on the current READING day, so a streak is not broken by the hour
	// between midnight and the cutoff.
	today, err := time.ParseInLocation("2006-01-02", readingDate(time.Now().Unix(), loc), loc)
	if err != nil {
		return 0
	}
	cursor := today
	if _, ok := idx.Days[today.Format("2006-01-02")]; !ok {
		cursor = today.AddDate(0, 0, -1)
	}
	n := 0
	for {
		if _, ok := idx.Days[cursor.Format("2006-01-02")]; !ok {
			return n
		}
		n++
		cursor = cursor.AddDate(0, 0, -1)
	}
}

func yearNav(years []int, active int) template.HTML {
	if len(years) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="field-row btn-row">`)
	for _, y := range years {
		kind := "secondary"
		if y == active {
			kind = "primary"
		}
		b.WriteString(string(webui.IconTextButton("", strconv.Itoa(y),
			webui.BtnOpts{Kind: kind, Size: "sm", Href: "/?year=" + strconv.Itoa(y)})))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// calendarGrid draws one year as columns of weeks.
//
// The intensity is banded rather than continuous. A linear ramp against the year's maximum
// makes an ordinary day invisible whenever one outlier exists — a single six-hour Sunday
// flattens three hundred honest half-hours to the palest shade. Five bands keyed to real
// durations mean a square's colour says the same thing in every year.
func calendarGrid(idx *readingIndex, year int, loc *time.Location) template.HTML {
	days, _ := idx.Year(year)

	start := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	end := time.Date(year, 12, 31, 0, 0, 0, 0, loc)
	// Back up to the Monday on or before 1 January so every column is a full week.
	for start.Weekday() != time.Monday {
		start = start.AddDate(0, 0, -1)
	}

	var b strings.Builder
	b.WriteString(`<div class="ko-calwrap"><div class="ko-cal">`)

	// Month labels along the top: ONE CELL PER WEEK COLUMN, carrying the month name only
	// in the week that month begins. The obvious version sets grid-column on each label to
	// position it — which is an inline style attribute, a construct dynlint holds down
	// precisely because it cannot be themed or overridden. Emitting an empty cell for every
	// other week puts the labels in the right columns by grid FLOW, with no positioning at
	// all, and the row stays aligned with the day grid below because both use the same
	// 13px auto-columns.
	b.WriteString(`<div class="ko-cal-months">`)
	lastMonth := -1
	for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
		label := ""
		if int(d.Month()) != lastMonth && d.Year() == year {
			label = d.Format("Jan")
			lastMonth = int(d.Month())
		}
		fmt.Fprintf(&b, `<span class="ko-cal-month">%s</span>`, label)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="ko-cal-grid">`)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		if d.Year() != year {
			b.WriteString(`<span class="ko-day ko-day-void"></span>`)
			continue
		}
		day := days[date]
		level := 0
		title := d.Format("Mon 2 Jan 2006") + " — nothing"
		if day != nil {
			level = intensity(day.Seconds)
			title = fmt.Sprintf("%s — %d min, %d pages", d.Format("Mon 2 Jan 2006"), day.Minutes(), day.Pages)
			if day.Highlights > 0 {
				title += fmt.Sprintf(", %d highlights", day.Highlights)
			}
		}
		mark := ""
		if day != nil && day.Highlights > 0 {
			mark = " ko-day-marked"
		}
		if day == nil {
			fmt.Fprintf(&b, `<span class="ko-day ko-l%d" title="%s"></span>`, level, template.HTMLEscapeString(title))
			continue
		}
		fmt.Fprintf(&b, `<a class="ko-day ko-l%d%s" href="/day?d=%s" title="%s"></a>`,
			level, mark, date, template.HTMLEscapeString(title))
	}
	b.WriteString(`</div></div></div>`)

	b.WriteString(`<div class="ko-legend"><span class="muted">Less</span>`)
	for l := 0; l <= 4; l++ {
		fmt.Fprintf(&b, `<span class="ko-day ko-l%d"></span>`, l)
	}
	b.WriteString(`<span class="muted">More</span><span class="muted ko-legend-note">A dot marks a day you highlighted something.</span></div>`)
	return template.HTML(b.String())
}

// intensity bands a day's reading time. Boundaries in minutes: 1, 15, 45, 120.
func intensity(seconds int) int {
	switch m := seconds / 60; {
	case m <= 0:
		return 0
	case m < 15:
		return 1
	case m < 45:
		return 2
	case m < 120:
		return 3
	default:
		return 4
	}
}

// serveDay is one day: what was read, and what was marked.
func (a *app) serveDay(w http.ResponseWriter, r *http.Request) {
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

	date := strings.TrimSpace(r.URL.Query().Get("d"))
	if _, perr := time.Parse("2006-01-02", date); perr != nil {
		date = readingDate(time.Now().Unix(), loc)
	}
	day := idx.Days[date]
	shown, _ := time.ParseInLocation("2006-01-02", date, loc)

	p := a.page("/")
	p.StartFor(w, r, "Reading — "+date)
	fmt.Fprint(w, webui.PageHeader(shown.Format("Monday 2 January 2006"), dayNavLine(date)))

	if day == nil {
		fmt.Fprint(w, `<div class="card"><p class="muted">Nothing read on this day.</p></div>`)
		p.End(w)
		return
	}

	fmt.Fprint(w, `<div class="card"><h3>Books</h3>`)
	rows := make([]webui.TableRow, 0, len(day.Books))
	for _, bd := range day.Books {
		rows = append(rows, webui.TableRow{Cells: []template.HTML{
			webui.Text(displayTitle(bd.Title)),
			webui.Text(orDash(bd.Authors)),
			webui.Text(fmt.Sprintf("%d min", maxInt(bd.Seconds/60, 1))),
			webui.Text(fmt.Sprintf("%d", bd.Pages)),
		}})
	}
	fmt.Fprint(w, webui.SimpleTable([]string{"Book", "Author", "Time", "Pages"}, rows))
	fmt.Fprintf(w, `<p class="muted">%d minutes across %d pages, between %s and %s.</p></div>`,
		day.Minutes(), day.Pages,
		time.Unix(day.FirstAt, 0).In(loc).Format("15:04"),
		time.Unix(day.LastAt, 0).In(loc).Format("15:04"))

	// What was marked that day.
	var marked []ReadEvent
	for _, an := range idx.Annotations {
		// THE SAME day definition the index used. Filtering annotations by calendar
		// date while the reading is bucketed by cutoff date would put a highlight and
		// the session it came from on different pages.
		if readingDate(an.StartTime, loc) == date {
			marked = append(marked, an)
		}
	}
	fmt.Fprint(w, `<div class="card"><h3>Marked on this day</h3>`)
	if len(marked) == 0 {
		fmt.Fprint(w, `<p class="muted">Nothing highlighted or bookmarked.</p>`)
	} else {
		// Tag from anywhere: each highlight carries the browser's own tag field, saved
		// to vision through /tag. Drawn only when vision's answer is known, so a field
		// never shows an empty set that is really "not loaded yet".
		anns, haveAnns := a.annotationsFor(caller)
		for _, an := range marked {
			fmt.Fprint(w, annotationBlock(an, loc, true))
			if haveAnns && strings.TrimSpace(an.Text) != "" {
				ref := quotebrowser.Ref{Source: sourceKoreader, ID: an.ID}
				fmt.Fprint(w, quotebrowser.TagControl(ref, anns[ref.String()].Tags, "/tag", r.URL.RequestURI()))
			}
		}
	}
	fmt.Fprint(w, `</div>`)
	p.End(w)
}

func dayNavLine(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s · %s", d.AddDate(0, 0, -1).Format("2006-01-02"), d.AddDate(0, 0, 1).Format("2006-01-02"))
}

// annotationBlock renders one quote through the shared browser's passage, so a quote on a
// day page looks exactly like one in the reader. The TEXT IS THE POINT, so it gets the
// room: the book and the time are a line under it, not a table row that clips it.
func annotationBlock(an ReadEvent, loc *time.Location, showTime bool) template.HTML {
	q := quotebrowser.Quote{Text: an.Text, Note: an.Note}
	pending := strings.TrimSpace(an.Text) == ""
	if pending {
		// SAYS WHAT IS HAPPENING. "A bookmark, with no quote" described the record and
		// not the situation: the page IS coming, the reader just has not synced enough
		// times to have fetched it yet. A placeholder that reads like a dead end sends
		// someone looking for a bug instead of pressing sync.
		q.Text = "A bookmark — its page has not been captured yet. It arrives on a future sync."
	}
	footer := displayTitle(an.Title)
	if strings.TrimSpace(an.Chapter) != "" {
		footer += " · " + an.Chapter
	}
	if an.Page > 0 {
		footer += fmt.Sprintf(" · p.%d", an.Page)
	}
	if showTime && an.StartTime > 0 {
		footer += " · " + time.Unix(an.StartTime, 0).In(loc).Format("15:04")
	}
	if pending {
		return quotebrowser.Passage(q, footer, pending)
	}
	// The source line opens THIS quote in its book's reader, which walks that book's
	// quotes in reading order with nothing truncated (owner feedback d3c929e9).
	href := "/quote?id=" + url.QueryEscape(sourceKoreader+":"+an.ID) + "&book=" + url.QueryEscape(an.BookMD5)
	return quotebrowser.PassageLinked(q, footer, href, pending)
}

// serveReview is the spaced-repetition queue.
func (a *app) serveReview(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	loc := a.deviceLoc(r.Context(), tk)

	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.FormValue("id"))
		switch r.FormValue("action") {
		case "suspend":
			_ = a.suspendCard(r.Context(), tk, id, loc)
		default:
			_ = a.recordReview(r.Context(), tk, id, Grade(r.FormValue("grade")), loc)
		}
		// POST-redirect-GET: a refresh after grading must not re-grade.
		http.Redirect(w, r, "/review", http.StatusSeeOther)
		return
	}

	idx, err := a.readingIndexFor(r.Context(), caller, tk)
	if err != nil {
		http.Error(w, "could not read your reading", http.StatusInternalServerError)
		return
	}
	today := time.Now().In(loc).Format("2006-01-02")
	due, total, err := a.dueCards(r.Context(), tk, idx, today, 1)
	if err != nil {
		http.Error(w, "could not read your review queue", http.StatusInternalServerError)
		return
	}

	p := a.page("/review")
	p.StartFor(w, r, "Review")

	if len(idx.Highlights()) == 0 {
		fmt.Fprint(w, `<div class="card"><h3>Nothing to review</h3>`)
		fmt.Fprint(w, `<p class="muted">Review works from your highlights, and none have synced yet. See <a href="/quotes">Quotes</a>.</p></div>`)
		p.End(w)
		return
	}
	if len(due) == 0 {
		fmt.Fprint(w, `<div class="card"><h3>Done for today</h3>`)
		fmt.Fprintf(w, `<p class="muted">Nothing is due. You have %d quotes in rotation — the next one comes back when it is time.</p>`, len(idx.Highlights()))
		fmt.Fprint(w, `<p class="muted"><a href="/quotes">Browse them all</a> in the meantime.</p></div>`)
		p.End(w)
		return
	}

	card := due[0]
	fmt.Fprintf(w, `<p class="muted">%d due today</p>`, total)
	fmt.Fprint(w, `<div class="card ko-review">`)
	fmt.Fprint(w, annotationBlock(card.Highlight, loc, false))

	// The grades. A form per button rather than one form with four submits, because the
	// stacked mobile layout puts them on separate lines and a shared form would need
	// name/value pairs that read as identical to a screen reader.
	fmt.Fprint(w, `<div class="field-row btn-row">`)
	for _, g := range []struct {
		grade Grade
		label string
		kind  string
	}{
		{GradeForgot, "Forgot", "danger"},
		{GradeHard, "Hard", "secondary"},
		{GradeGood, "Good", "primary"},
		{GradeEasy, "Easy", "secondary"},
	} {
		fmt.Fprintf(w, `<form method="post" class="ko-grade"><input type="hidden" name="id" value="%s"><input type="hidden" name="grade" value="%s">%s</form>`,
			template.HTMLEscapeString(card.Card.ID), template.HTMLEscapeString(string(g.grade)),
			webui.IconTextButton("", g.label, webui.BtnOpts{Kind: g.kind, Type: "submit"}))
	}
	fmt.Fprint(w, `</div>`)

	// ITS OWN .field-row.btn-row. Emitted bare, this form had no .btn-row and therefore
	// none of the top margin that supplies a control row's clearance, so the button sat
	// against the grade row's edge. The gap belongs to the container (STYLEGUIDE §3) —
	// which means a second row of controls needs a second container, not a bare element
	// after the first one.
	fmt.Fprintf(w, `<div class="field-row btn-row"><form method="post"><input type="hidden" name="id" value="%s"><input type="hidden" name="action" value="suspend">%s</form></div>`,
		template.HTMLEscapeString(card.Card.ID),
		webui.IconTextButton("fa-solid fa-ban", "Never show this again",
			webui.BtnOpts{Kind: "secondary", Size: "sm", Type: "submit"}))

	if card.New {
		fmt.Fprint(w, `<p class="muted">New quote.</p>`)
	} else {
		fmt.Fprintf(w, `<p class="muted">Seen %d times, due since %s.</p>`, card.Card.Reps, webui.Text(card.Card.DueOn))
	}
	fmt.Fprint(w, `</div>`)
	p.End(w)
}

// displayTitle is how a book's title is shown.
//
// A PLACEHOLDER AUTHOR IS DROPPED. KOReader titles a PDF with no metadata from its file
// name, and files named "Author - Title" by a library tool keep the author slot even when
// the tool never knew it — "Unknown - The Slight Edge". Shown in a narrow column that is
// just "Unknown". Only a placeholder is dropped: plenty of real titles contain " - ".
func displayTitle(s string) string {
	s = strings.TrimSpace(s)
	if before, after, ok := strings.Cut(s, " - "); ok && strings.TrimSpace(after) != "" {
		switch strings.ToLower(strings.TrimSpace(before)) {
		case "unknown", "unknown author", "n/a", "anonymous":
			s = strings.TrimSpace(after)
		}
	}
	if s == "" {
		return "Untitled"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
