package main

// yearimg.go — the year as a picture to keep and share: the twelve month calendars the page shows
// (a box per day, a bar per book, a histogram of the hours) with the year's reading numbers above and
// the most-read books below.
//
// Drawn in Go with the standard library only: an RGBA image, rectangles and an embedded typeface
// (exportfont.go), so it needs no browser and no outside tool, and a year is a few tens of
// milliseconds of work. Colours are the house dark palette, fixed, so a saved picture looks
// the same wherever it is opened.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"time"
)

const (
	imgU       = 2   // device pixels per layout unit: the picture is drawn at 2x so small text stays sharp
	imgW       = 900 // layout units
	imgMargin  = 40
	imgCellGap = 2
	imgCellW   = (imgW - 2*imgMargin - 6*imgCellGap) / 7 // one month per row, the full width: a box as wide as the month page's
	imgLane    = 10                                      // a book's bar and the room it takes
	imgNumH    = 13                                      // the day number's line
	imgRow     = 22 // a most-read row
	imgTickH   = 12                                      // the foot: the hours histogram
	imgCellH   = imgNumH + maxLanes*imgLane + imgTickH + 2
)

// The house palette (dark theme), as the page's tokens resolve there.
var (
	imgBG     = color.RGBA{0x0d, 0x11, 0x17, 255}
	imgCard   = color.RGBA{0x16, 0x1b, 0x22, 255}
	imgBorder = color.RGBA{0x30, 0x36, 0x3d, 255}
	imgText   = color.RGBA{0xe6, 0xed, 0xf3, 255}
	imgMuted  = color.RGBA{0x8b, 0x94, 0x9e, 255}
	imgAccent = color.RGBA{0x58, 0xa6, 0xff, 255}
	imgTones  = [calTones]color.RGBA{
		{0x58, 0xa6, 0xff, 255}, {0x3f, 0xb9, 0x50, 255}, {0xbc, 0x8c, 0xff, 255}, {0xd2, 0x99, 0x22, 255}, {0xf7, 0x78, 0xba, 255},
	}
)

func blend(a, b color.RGBA, pct int) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8((int(x)*pct + int(y)*(100-pct)) / 100) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 255}
}

// canvas is an RGBA image the pictures are drawn on.
type canvas struct {
	img *image.RGBA
}

func newCanvas(wu, hu int) *canvas {
	return &canvas{img: image.NewRGBA(image.Rect(0, 0, wu*imgU, hu*imgU))}
}

// px fills a rectangle in device pixels: one row is written, the rest are copies of it.
func (c *canvas) px(x, y, w, h int, col color.RGBA) {
	b := c.img.Bounds()
	x0, x1 := max(x, 0), min(x+w, b.Dx())
	y0, y1 := max(y, 0), min(y+h, b.Dy())
	if x0 >= x1 || y0 >= y1 {
		return
	}
	first := c.img.Pix[y0*c.img.Stride+x0*4 : y0*c.img.Stride+x1*4]
	for o := 0; o < len(first); o += 4 {
		first[o], first[o+1], first[o+2], first[o+3] = col.R, col.G, col.B, 255
	}
	for yy := y0 + 1; yy < y1; yy++ {
		copy(c.img.Pix[yy*c.img.Stride+x0*4:yy*c.img.Stride+x1*4], first)
	}
}

// over blends col onto one pixel at coverage a (0..1).
func (c *canvas) over(x, y int, col color.RGBA, a float32) {
	if !(image.Point{x, y}).In(c.img.Bounds()) {
		return
	}
	o := y*c.img.Stride + x*4
	p := c.img.Pix[o : o+4]
	m := func(d, s uint8) uint8 { return uint8(float32(d) + (float32(s)-float32(d))*a + 0.5) }
	p[0], p[1], p[2], p[3] = m(p[0], col.R), m(p[1], col.G), m(p[2], col.B), 255
}

// rect fills a rectangle in layout units.
func (c *canvas) rect(x, y, w, h int, col color.RGBA) { c.px(x*imgU, y*imgU, w*imgU, h*imgU, col) }

func (c *canvas) stroke(x, y, w, h int, col color.RGBA) {
	c.rect(x, y, w, 1, col)
	c.rect(x, y+h-1, w, 1, col)
	c.rect(x, y, 1, h, col)
	c.rect(x+w-1, y, 1, h, col)
}

// The type: scale is the size step the pictures were laid out in. A step is ten layout units of em
// (capitals seven tall), and the big steps are bold.
const (
	emPerScale = 10
	capPerEm   = 0.7 // Liberation Sans' capitals are 0.69 of the em
)

func scaleBold(scale int) bool { return scale >= 4 }

// text draws s with its capitals' top at y (layout units), in the typeface of the exports.
func (c *canvas) text(x, y, scale int, s string, col color.RGBA) {
	f := exportFace(scaleBold(scale))
	if f == nil {
		return
	}
	em := float64(emPerScale * scale * imgU)
	f.drawText(c, float64(x*imgU), float64(y*imgU)+em*capPerEm, em, s, col)
}

// textW is the width of s in layout units, rounded up.
func textW(s string, scale int) int {
	f := exportFace(scaleBold(scale))
	if f == nil {
		return len([]rune(s)) * 6 * scale
	}
	return int(math.Ceil(f.textAdvance(s, float64(emPerScale*scale*imgU)) / imgU))
}

// fit shortens s to the widest start of it that fits in maxW layout units at scale, with a
// dot-dot-dot when it had to.
func fit(s string, scale, maxW int) string {
	if textW(s, scale) <= maxW {
		return s
	}
	r := []rune(s)
	for n := len(r) - 1; n > 1; n-- {
		if t := strings.TrimRight(string(r[:n]), " ") + "..."; textW(t, scale) <= maxW {
			return t
		}
	}
	return "..."
}

// bookLine is a most-read row: its number and the title, cut to what is left of the row once the
// time at its right edge has its room.
func bookLine(n int, title, t string) string {
	prefix := fmt.Sprintf("%d. ", n)
	return prefix + fit(displayTitle(title), 2, imgW-2*imgMargin-textW(t, 2)-textW(prefix, 2)-16)
}

// yearPNG draws the year. now fixes "today" so a test is stable.
func yearPNG(idx *readingIndex, loc *time.Location, year int, now time.Time) ([]byte, error) {
	st := buildYearStats(idx, loc, year)
	const head = 36 + 35 + 14 + 62 + 26
	const monthHead = 14 + 6 + 7 + 4 // the month's name, then the weekday names
	const monthGap = 22
	// A month is as tall as its weeks, so one of four weeks takes less room than one of six.
	cals := make([]monthCal, 12)
	months := 0
	for m := 1; m <= 12; m++ {
		cals[m-1] = buildMonthCal(idx, loc, time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc), now)
		months += monthHead + len(cals[m-1].Weeks)*(imgCellH+imgCellGap) + monthGap
	}
	foot := 14 + 8 + len(st.Top)*imgRow + 40
	if len(st.Top) == 0 {
		foot = 40
	}
	hu := head + months + foot
	c := newCanvas(imgW, hu)
	c.rect(0, 0, imgW, hu, imgBG)

	c.text(imgMargin, 36, 5, fmt.Sprintf("Reading %d", year), imgText)

	// The numbers.
	tiles := [][2]string{
		{fmt.Sprint(st.Days), "Days read"}, {fmt.Sprintf("%.0f", float64(st.Seconds)/3600), "Hours"},
		{fmt.Sprint(st.Books), "Books"}, {fmt.Sprint(st.Pages), "Pages"},
		{fmt.Sprint(st.Quotes), "Quotes"}, {fmt.Sprint(st.Streak), "Best streak"},
	}
	tw := (imgW - 2*imgMargin - 5*12) / 6
	for i, t := range tiles {
		x, y := imgMargin+i*(tw+12), 36+35+14
		c.rect(x, y, tw, 62, imgCard)
		c.stroke(x, y, tw, 62, imgBorder)
		c.text(x+10, y+8, 4, t[0], imgText)
		c.text(x+10, y+42, 2, t[1], imgMuted)
	}

	// The twelve months, one to a row, each the month page's calendar at the full width.
	y := head
	for m := 1; m <= 12; m++ {
		mc := cals[m-1]
		x := imgMargin
		c.text(x, y, 2, time.Month(m).String(), imgText)
		minTxt := fmt.Sprintf("%d min", mc.Minutes)
		c.text(imgW-imgMargin-textW(minTxt, 1), y+4, 1, minTxt, imgMuted)
		hy := y + 14 + 6
		for d, n := range calDayNames {
			c.text(x+d*(imgCellW+imgCellGap)+(imgCellW-textW(n, 1))/2, hy, 1, n, imgMuted)
		}
		for wi, w := range mc.Weeks {
			drawWeek(c, x, hy+7+4+wi*(imgCellH+imgCellGap), w)
		}
		y += monthHead + len(mc.Weeks)*(imgCellH+imgCellGap) + monthGap
	}

	// The most-read books.
	if len(st.Top) > 0 {
		y += 6
		c.text(imgMargin, y, 2, "Most read", imgMuted)
		for i, b := range st.Top {
			ly := y + 14 + 8 + i*imgRow
			t := humanDuration(b.Seconds)
			c.text(imgMargin, ly, 2, bookLine(i+1, b.Title, t), imgText)
			c.text(imgW-imgMargin-textW(t, 2), ly, 2, t, imgMuted)
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawWeek draws seven day boxes and the bars and histograms over them, the way the page does.
func drawWeek(c *canvas, x, y int, w calWeek) {
	cx := func(col int) int { return x + (col-1)*(imgCellW+imgCellGap) }
	for i, d := range w.Days {
		if !d.InMonth {
			continue
		}
		bx := cx(i + 1)
		c.rect(bx, y, imgCellW, imgCellH, imgCard)
		border, num := imgBorder, imgText
		if d.Today {
			border, num = imgAccent, imgAccent
		}
		c.stroke(bx, y, imgCellW, imgCellH, border)
		c.text(bx+3, y+3, 1, fmt.Sprint(d.Num), num)
		// The foot of the box: 24 hours, midnight to midnight, a column each across the box.
		base := (y + imgCellH - 2) * imgU
		colW := (imgCellW - 10) * imgU / 24
		for h, k := range d.Ticks {
			tx := (bx+5)*imgU + h*colW
			if k == 0 {
				if d.Minutes > 0 {
					c.px(tx, base-1, colW-1, 1, blend(imgMuted, imgCard, 30))
				}
				continue
			}
			th := []int{0, 25, 50, 75, 100}[k] * (imgTickH - 2) * imgU / 100
			c.px(tx, base-th, colW-1, th, imgMuted)
		}
	}
	for _, b := range w.Bars {
		bx := cx(b.Col) + 1
		bw := cx(b.Col+b.Span-1) + imgCellW - 1 - bx
		by := y + imgNumH + (b.Lane-1)*imgLane
		tone := imgTones[b.Tone%calTones]
		c.rect(bx, by, bw, imgLane-2, blend(tone, imgCard, 35))
		c.rect(bx, by, 3, imgLane-2, tone)
		// The book's title, as the page prints it on the bar, cut to what the bar can hold.
		c.text(bx+6, by+1, 1, fit(displayTitle(b.Title), 1, bw-8), imgText)
	}
}
