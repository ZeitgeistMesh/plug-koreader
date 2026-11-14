package main

import (
	"bytes"
	"os"
	"image/png"
	"strings"
	"testing"
	"time"
)

func yearFixture() (*readingIndex, *time.Location) {
	loc := time.UTC
	at := func(d string, h int) int64 {
		t, _ := time.ParseInLocation("2006-01-02 15", d+" "+itoa2(h), loc)
		return t.Unix()
	}
	ev := []ReadEvent{
		{BookMD5: "a", Title: "Dune", StartTime: at("2026-03-02", 9), Duration: 1800, Page: 1},
		{BookMD5: "a", Title: "Dune", StartTime: at("2026-03-03", 21), Duration: 3600, Page: 2},
		{BookMD5: "b", Title: "Emma", StartTime: at("2026-03-04", 7), Duration: 600, Page: 1},
		{BookMD5: "b", Title: "Emma", StartTime: at("2026-11-12", 20), Duration: 900, Page: 4},
	}
	return buildReadingIndex(ev, loc), loc
}

func itoa2(h int) string {
	if h < 10 {
		return "0" + string(rune('0'+h))
	}
	return string(rune('0'+h/10)) + string(rune('0'+h%10))
}

func TestYearStatsCountTheYearsReading(t *testing.T) {
	idx, loc := yearFixture()
	st := buildYearStats(idx, loc, 2026)
	if st.Days != 4 || st.Books != 2 || st.Streak != 3 || st.Seconds != 6900 {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.Top) != 2 || st.Top[0].Title != "Dune" {
		t.Errorf("top = %+v", st.Top)
	}
	if o := buildYearStats(idx, loc, 2025); o.Days != 0 || len(o.Top) != 0 {
		t.Errorf("another year must be empty: %+v", o)
	}
}

// The Year page shows twelve months in the month-calendar style, with a selector and a way to save the picture.
func TestYearPageIsTwelveMonthCalendarsWithASelector(t *testing.T) {
	idx, loc := yearFixture()
	h := string(renderYear(idx, loc, 2026, time.Date(2026, 11, 14, 12, 0, 0, 0, loc)))
	if n := strings.Count(h, `class="ko-ymonth"`); n != 12 {
		t.Errorf("months = %d, want 12", n)
	}
	for _, want := range []string{`href="/calendar?m=2026-03"`, `hx-get="/year?y=2026"`, `/year.png?y=2026&amp;dl=1`, `download="reading-2026.png"`, `ko-ticks`, `ko-bar`} {
		if !strings.Contains(h, want) {
			t.Errorf("year page lacks %s", want)
		}
	}
}

func TestMonthCalendarYearButtonGoesToTheYearPage(t *testing.T) {
	idx, loc := yearFixture()
	h := string(renderMonthCal(buildMonthCal(idx, loc, time.Date(2026, 3, 1, 0, 0, 0, 0, loc), time.Now())))
	if !strings.Contains(h, `href="/year?y=2026"`) {
		t.Error("the Year button must open /year for the month's year")
	}
}

func TestYearPNGIsAValidPictureAndFast(t *testing.T) {
	idx, loc := yearFixture()
	start := time.Now()
	body, err := yearPNG(idx, loc, 2026, time.Date(2026, 11, 14, 12, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a year took %v", d)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != imgW*imgU || b.Dy() < 2000 {
		t.Errorf("size = %v", b)
	}
	// Different data draws different pixels: a year with reading differs from an empty one.
	empty, _ := yearPNG(idx, loc, 2025, time.Date(2026, 11, 14, 12, 0, 0, 0, loc))
	if bytes.Equal(body, empty) {
		t.Error("the picture does not depend on the year")
	}
}

// Owner via master: the year is seen at once, four months a row in the wide column.
func TestYearPageIsFourMonthsARowAndWide(t *testing.T) {
	if !strings.Contains(yearCSS, "repeat(4,minmax(0,1fr))") {
		t.Error("the year page must lay out four months a row")
	}
	if !strings.Contains(yearCSS, ".ko-ymonth .ko-mcal{--ko-row:.3rem}") {
		t.Error("the row height must be set on .ko-mcal, where the month page sets it (a value on an ancestor is overridden)")
	}
	src, err := os.ReadFile("yearcal.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "flowweb.WidthWide") {
		t.Error("the year page needs the wide column")
	}
}
