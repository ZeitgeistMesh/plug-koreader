package main

// yearimg.go — the year as a picture to keep and share: the twelve month calendars the page shows
// (a box per day, a bar per book, a histogram of the hours) with the year's reading numbers above and
// the most-read books below.
//
// Drawn in Go with the standard library only: an indexed-colour image, rectangles and a small
// built-in bitmap font, so it needs no font file, no browser and no outside tool, and a year is
// a few milliseconds of work. Colours are the house dark palette, fixed, so a saved picture looks
// the same wherever it is opened.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"time"
)

const (
	imgU        = 2    // device pixels per layout unit: the picture is drawn at 2x so small text stays sharp
	imgW        = 1200 // layout units
	imgMargin   = 40
	imgCols     = 4
	imgCellW    = 34
	imgCellH    = 48
	imgCellGap  = 2
	imgMonthW   = 7*imgCellW + 6*imgCellGap
	imgMonthGap = (imgW - 2*imgMargin - imgCols*imgMonthW) / (imgCols - 1)
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

// canvas is an indexed image with a palette that grows as colours are first used.
type canvas struct {
	img *image.Paletted
	pal color.Palette
	idx map[color.RGBA]uint8
}

func newCanvas(wu, hu int) *canvas {
	return &canvas{img: image.NewPaletted(image.Rect(0, 0, wu*imgU, hu*imgU), nil), idx: map[color.RGBA]uint8{}}
}

func (c *canvas) ci(col color.RGBA) uint8 {
	if i, ok := c.idx[col]; ok {
		return i
	}
	i := uint8(len(c.pal))
	c.pal = append(c.pal, col)
	c.idx[col] = i
	return i
}

// px fills a rectangle in device pixels.
func (c *canvas) px(x, y, w, h int, col color.RGBA) {
	i := c.ci(col)
	b := c.img.Bounds()
	for yy := max(y, 0); yy < min(y+h, b.Dy()); yy++ {
		row := c.img.Pix[yy*c.img.Stride:]
		for xx := max(x, 0); xx < min(x+w, b.Dx()); xx++ {
			row[xx] = i
		}
	}
}

// rect fills a rectangle in layout units.
func (c *canvas) rect(x, y, w, h int, col color.RGBA) { c.px(x*imgU, y*imgU, w*imgU, h*imgU, col) }

func (c *canvas) stroke(x, y, w, h int, col color.RGBA) {
	c.rect(x, y, w, 1, col)
	c.rect(x, y+h-1, w, 1, col)
	c.rect(x, y, 1, h, col)
	c.rect(x+w-1, y, 1, h, col)
}

// text draws s (upper-cased) with the built-in font; scale is layout units per font pixel.
func (c *canvas) text(x, y, scale int, s string, col color.RGBA) {
	for _, r := range strings.ToUpper(s) {
		g, ok := glyphs[r]
		if !ok {
			g = glyphs['?']
		}
		for row, line := range strings.Split(g, "|") {
			for cx, ch := range line {
				if ch == '#' {
					c.rect(x+cx*scale, y+row*scale, scale, scale, col)
				}
			}
		}
		x += 6 * scale
	}
}

func textW(s string, scale int) int { return len([]rune(s)) * 6 * scale }

// fit shortens s to at most n characters, with a dot-dot-dot when it had to.
func fit(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:max(n-3, 1)]) + "..."
}

// yearPNG draws the year. now fixes "today" so a test is stable.
func yearPNG(idx *readingIndex, loc *time.Location, year int, now time.Time) ([]byte, error) {
	st := buildYearStats(idx, loc, year)
	const head = 36 + 35 + 14 + 62 + 26
	monthH := 14 + 6 + 7 + 4 + 6*(imgCellH+imgCellGap)
	months := 3 * (monthH + 20)
	foot := 14 + 8 + len(st.Top)*18 + 40
	if len(st.Top) == 0 {
		foot = 40
	}
	hu := head + months + foot
	c := newCanvas(imgW, hu)
	c.rect(0, 0, imgW, hu, imgBG)

	c.text(imgMargin, 36, 5, fmt.Sprintf("Reading %d", year), imgText)

	// The numbers.
	tiles := [][2]string{
		{fmt.Sprint(st.Days), "DAYS READ"}, {fmt.Sprintf("%.0f", float64(st.Seconds)/3600), "HOURS"},
		{fmt.Sprint(st.Books), "BOOKS"}, {fmt.Sprint(st.Pages), "PAGES"},
		{fmt.Sprint(st.Quotes), "QUOTES"}, {fmt.Sprint(st.Streak), "BEST STREAK"},
	}
	tw := (imgW - 2*imgMargin - 5*12) / 6
	for i, t := range tiles {
		x, y := imgMargin+i*(tw+12), 36+35+14
		c.rect(x, y, tw, 62, imgCard)
		c.stroke(x, y, tw, 62, imgBorder)
		c.text(x+10, y+8, 4, t[0], imgText)
		c.text(x+10, y+42, 2, t[1], imgMuted)
	}

	// The twelve months.
	y0 := head
	for m := 1; m <= 12; m++ {
		col, row := (m-1)%imgCols, (m-1)/imgCols
		x := imgMargin + col*(imgMonthW+imgMonthGap)
		y := y0 + row*(monthH+20)
		mc := buildMonthCal(idx, loc, time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc), now)
		c.text(x, y, 2, time.Month(m).String(), imgText)
		c.text(x+imgMonthW-textW(fmt.Sprintf("%d MIN", mc.Minutes), 1), y+4, 1, fmt.Sprintf("%d MIN", mc.Minutes), imgMuted)
		hy := y + 14 + 6
		for d, n := range calDayNames {
			c.text(x+d*(imgCellW+imgCellGap)+(imgCellW-textW(n, 1))/2, hy, 1, n, imgMuted)
		}
		for wi, w := range mc.Weeks {
			wy := hy + 7 + 4 + wi*(imgCellH+imgCellGap)
			drawWeek(c, x, wy, w)
		}
	}

	// The most-read books.
	if len(st.Top) > 0 {
		y := y0 + months + 6
		c.text(imgMargin, y, 2, "MOST READ", imgMuted)
		for i, b := range st.Top {
			line := fmt.Sprintf("%d. %s", i+1, fit(displayTitle(b.Title), 70))
			ly := y + 14 + 8 + i*18
			c.text(imgMargin, ly, 2, line, imgText)
			t := humanDuration(b.Seconds)
			c.text(imgW-imgMargin-textW(t, 2), ly, 2, t, imgMuted)
		}
	}

	c.img.Palette = c.pal
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
		// The foot of the box: 24 hours, midnight to midnight, a pixel column each.
		base := y + imgCellH - 2
		for h, k := range d.Ticks {
			tx := (bx + 5) * imgU + h*imgU
			if k == 0 {
				if d.Minutes > 0 {
					c.px(tx, base*imgU-1, imgU, 1, blend(imgMuted, imgCard, 30))
				}
				continue
			}
			th := []int{0, 25, 50, 75, 100}[k] * 10 * imgU / 100
			c.px(tx, base*imgU-th, imgU, th, imgMuted)
		}
	}
	for _, b := range w.Bars {
		bx := cx(b.Col) + 1
		bw := cx(b.Col+b.Span-1) + imgCellW - 1 - bx
		by := y + 13 + (b.Lane-1)*6
		tone := imgTones[b.Tone%calTones]
		c.rect(bx, by, bw, 5, blend(tone, imgCard, 35))
		c.rect(bx, by, 2, 5, tone)
	}
}

// glyphs is a 5x7 bitmap font: capitals, digits and the few marks the picture uses. Rows are
// separated by "|", "#" is ink.
var glyphs = map[rune]string{
	' ': ".....|.....|.....|.....|.....|.....|.....",
	'A': ".###.|#...#|#...#|#####|#...#|#...#|#...#",
	'B': "####.|#...#|#...#|####.|#...#|#...#|####.",
	'C': ".###.|#...#|#....|#....|#....|#...#|.###.",
	'D': "####.|#...#|#...#|#...#|#...#|#...#|####.",
	'E': "#####|#....|#....|####.|#....|#....|#####",
	'F': "#####|#....|#....|####.|#....|#....|#....",
	'G': ".###.|#...#|#....|#.###|#...#|#...#|.###.",
	'H': "#...#|#...#|#...#|#####|#...#|#...#|#...#",
	'I': ".###.|..#..|..#..|..#..|..#..|..#..|.###.",
	'J': "..###|...#.|...#.|...#.|...#.|#..#.|.##..",
	'K': "#...#|#..#.|#.#..|##...|#.#..|#..#.|#...#",
	'L': "#....|#....|#....|#....|#....|#....|#####",
	'M': "#...#|##.##|#.#.#|#.#.#|#...#|#...#|#...#",
	'N': "#...#|##..#|#.#.#|#..##|#...#|#...#|#...#",
	'O': ".###.|#...#|#...#|#...#|#...#|#...#|.###.",
	'P': "####.|#...#|#...#|####.|#....|#....|#....",
	'Q': ".###.|#...#|#...#|#...#|#.#.#|#..#.|.##.#",
	'R': "####.|#...#|#...#|####.|#.#..|#..#.|#...#",
	'S': ".####|#....|#....|.###.|....#|....#|####.",
	'T': "#####|..#..|..#..|..#..|..#..|..#..|..#..",
	'U': "#...#|#...#|#...#|#...#|#...#|#...#|.###.",
	'V': "#...#|#...#|#...#|#...#|#...#|.#.#.|..#..",
	'W': "#...#|#...#|#...#|#.#.#|#.#.#|##.##|#...#",
	'X': "#...#|#...#|.#.#.|..#..|.#.#.|#...#|#...#",
	'Y': "#...#|#...#|.#.#.|..#..|..#..|..#..|..#..",
	'Z': "#####|....#|...#.|..#..|.#...|#....|#####",
	'0': ".###.|#...#|#..##|#.#.#|##..#|#...#|.###.",
	'1': "..#..|.##..|..#..|..#..|..#..|..#..|.###.",
	'2': ".###.|#...#|....#|...#.|..#..|.#...|#####",
	'3': "####.|....#|....#|.###.|....#|....#|####.",
	'4': "...#.|..##.|.#.#.|#..#.|#####|...#.|...#.",
	'5': "#####|#....|####.|....#|....#|#...#|.###.",
	'6': ".###.|#....|#....|####.|#...#|#...#|.###.",
	'7': "#####|....#|...#.|..#..|.#...|.#...|.#...",
	'8': ".###.|#...#|#...#|.###.|#...#|#...#|.###.",
	'9': ".###.|#...#|#...#|.####|....#|....#|.###.",
	'.': ".....|.....|.....|.....|.....|.....|..#..",
	',': ".....|.....|.....|.....|.....|..#..|.#...",
	':': ".....|..#..|.....|.....|..#..|.....|.....",
	'-': ".....|.....|.....|.###.|.....|.....|.....",
	'/': "....#|....#|...#.|..#..|.#...|#....|#....",
	'%': "##..#|##..#|...#.|..#..|.#...|#..##|#..##",
	'\'': "..#..|..#..|.#...|.....|.....|.....|.....",
	'&': ".##..|#..#.|#.#..|.#...|#.#.#|#..#.|.##.#",
	'(': "..#..|.#...|#....|#....|#....|.#...|..#..",
	')': "..#..|...#.|....#|....#|....#|...#.|..#..",
	'?': ".###.|#...#|....#|...#.|..#..|.....|..#..",
	'!': "..#..|..#..|..#..|..#..|..#..|.....|..#..",
	'+': ".....|..#..|..#..|#####|..#..|..#..|.....",
}

func init() {
	// Every glyph must be seven rows of five, or the drawing silently skews.
	for r, g := range glyphs {
		rows := strings.Split(g, "|")
		ok := len(rows) == 7
		for _, row := range rows {
			ok = ok && len(row) == 5
		}
		if !ok {
			panic(fmt.Sprintf("yearimg: bad glyph %q", r))
		}
	}
}
