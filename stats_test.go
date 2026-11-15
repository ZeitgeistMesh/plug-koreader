package main

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"
)

// The Stats page has the year page's selector and picture button, the year's numbers, and bars by weekday,
// hour and month.
func TestStatsPageHasTheYearBarAndTheNumbers(t *testing.T) {
	idx, loc := yearFixture()
	h := string(renderStats(idx, loc, 2026, time.Date(2026, 11, 14, 12, 0, 0, 0, loc)))
	for _, want := range []string{`id="ko-stats"`, `hx-get="/stats?y=2026"`, `hx-select="#ko-stats"`, `/stats.png?y=2026&amp;dl=1`,
		`download="reading-stats-2026.png"`, statsBooksRead, "March", "From 2 Mar to 4 Mar", "Dune", statsAvgDay, statsWeekdayHead, statsHourHead, statsMonthHead,
		"You read most on Tuesdays.", "Your best hour is 9 pm."} {
		if !strings.Contains(h, want) {
			t.Errorf("stats page lacks %q", want)
		}
	}
	if strings.Contains(h, "£") {
		t.Error("a reading page shows no money")
	}
}

func TestStatsDetailCountsByWeekdayHourAndMonth(t *testing.T) {
	idx, loc := yearFixture()
	d := buildYearDetail(idx, loc, 2026)
	// 2 Mar is a Monday: 1800s; Tue 3 Mar 3600s; Wed 4 Mar 600s; Thu 12 Nov 900s.
	if d.Weekday[0] != 1800 || d.Weekday[1] != 3600 || d.Weekday[2] != 600 || d.Weekday[3] != 900 {
		t.Errorf("weekday = %v", d.Weekday)
	}
	if d.Hour[9] != 1800 || d.Hour[21] != 3600 || d.Hour[7] != 600 || d.Hour[20] != 900 {
		t.Errorf("hour = %v", d.Hour)
	}
	if d.BusiestMonth != 3 || d.Month[2] != 6000 || d.Month[10] != 900 {
		t.Errorf("month = %v busiest %d", d.Month, d.BusiestMonth)
	}
	if d.StreakFrom != "2 Mar" || d.StreakTo != "4 Mar" {
		t.Errorf("streak %s to %s", d.StreakFrom, d.StreakTo)
	}
	if e := string(renderStats(idx, loc, 2025, time.Date(2026, 11, 14, 12, 0, 0, 0, loc))); !strings.Contains(e, "Nothing read in 2025.") || strings.Contains(e, statsHourHead) {
		t.Errorf("an empty year: %s", e)
	}
}

func TestStatsPNGIsAPictureForAFullAndAnEmptyYear(t *testing.T) {
	idx, loc := yearFixture()
	for _, y := range []int{2026, 2025} {
		b, err := statsPNG(idx, loc, y)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != imgW*imgU {
			t.Errorf("year %d: %v %v", y, err, img.Bounds())
		}
	}
}

// A tile says its words in full, on the page and in the picture: the picture's widest label and number fit the tile.
func TestStatsTilesFitTheirWords(t *testing.T) {
	tw := (imgW - 2*imgMargin - 3*12) / 4
	for _, l := range []string{statsDaysRead, statsHours, statsBooksRead, statsPages, statsQuotes, statsBestStreak, statsBusiestMonth, statsAvgDay} {
		if textW(l, 2) > tw-20 {
			t.Errorf("%q does not fit a tile at the label size", l)
		}
	}
	for _, v := range []string{"September", "Wednesday", "99999", "120 min"} {
		if textW(v, 2) > tw-20 {
			t.Errorf("%q does not fit a tile even at the smallest number size", v)
		}
	}
	idx, loc := yearFixture()
	h := string(renderStats(idx, loc, 2026, time.Date(2026, 11, 14, 12, 0, 0, 0, loc)))
	if !strings.Contains(h, `title="Dune"`) || !strings.Contains(h, "Daily average") || !strings.Contains(h, "min") {
		t.Errorf("the page lacks a title tooltip or the average's unit: %s", h)
	}
}

// The Year and Stats pages share one bar (owner, cc3e26a6): the two views to switch between, the years, and
// the View + Save capsule; the calendar's bar has Year only.
func TestYearAndStatsShareOneBar(t *testing.T) {
	idx, loc := yearFixture()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, loc)
	for _, h := range []string{string(renderYear(idx, loc, 2026, now)), string(renderStats(idx, loc, 2026, now))} {
		for _, want := range []string{`href="/year?y=2026"`, `href="/stats?y=2026"`, `hx-get="/`} {
			if !strings.Contains(h, want) {
				t.Errorf("a bar lacks %s", want)
			}
		}
	}
	c := string(renderMonthCal(buildMonthCal(idx, loc, time.Date(2026, 3, 1, 0, 0, 0, 0, loc), now)))
	if !strings.Contains(c, `href="/year?y=2026"`) || strings.Contains(c, "/stats") {
		t.Error("the calendar bar keeps Year and has no Stats")
	}
}

// Owner (dab02a73, e81b1fce): the picture's two actions are one capsule of two links. View opens the picture
// in the browser (no download flag, a new tab); Save downloads it. The same on Year and Stats.
func TestYearAndStatsHaveAViewAndSaveCapsule(t *testing.T) {
	idx, loc := yearFixture()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, loc)
	for png, h := range map[string]string{"/year.png": string(renderYear(idx, loc, 2026, now)), "/stats.png": string(renderStats(idx, loc, 2026, now))} {
		if strings.Contains(h, "Save as image") {
			t.Errorf("%s: the single Save as image button is still there", png)
		}
		i := strings.Index(h, `class="btn-group"><a`)
		for i >= 0 && !strings.Contains(h[i:min(i+900, len(h))], png) {
			j := strings.Index(h[i+1:], `class="btn-group"><a`)
			if j < 0 {
				i = -1
				break
			}
			i += 1 + j
		}
		if i < 0 {
			t.Errorf("%s: no button group holds the picture links", png)
			continue
		}
		cap := h[i : i+strings.Index(h[i:], "</div>")]
		view, save := strings.Index(cap, `href="`+png+`?y=2026"`), strings.Index(cap, `href="`+png+`?y=2026&amp;dl=1"`)
		if view < 0 || save < 0 || view > save {
			t.Errorf("%s: want View then Save in one capsule: %s", png, cap)
		}
		if !strings.Contains(cap, `target="_blank"`) || strings.Count(cap, "download=") != 1 {
			t.Errorf("%s: View opens a new tab and only Save downloads: %s", png, cap)
		}
		if !strings.Contains(cap, ">View<") && !strings.Contains(cap, "View</") {
			t.Errorf("%s: the capsule's first link is not called View", png)
		}
	}
}
