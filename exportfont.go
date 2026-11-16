package main

// exportfont.go — the typeface of the exported pictures (year, stats): Noto Sans Mono, embedded
// (SIL Open Font License 1.1, fonts/LICENSE-NotoSansMono). The owner asked for "OpenSans Mono Book"
// (dab02a73 18901aa3); no font has that name, and Noto Sans Mono is the monospaced face of the Noto
// family that grew from Open Sans, in its regular ("book") weight. It is drawn with anti-aliasing
// by a small TrueType reader and rasteriser written against the standard library only. It reads the tables a Latin,
// Greek or Cyrillic line needs (head, maxp, hhea, hmtx, cmap format 4, loca, glyf with composites)
// and fills each outline by exact area coverage, so a picture needs no font tool, no browser and no
// module outside the standard library. There is no kerning or hinting: at the sizes a shared
// picture is read at, neither shows.

import (
	_ "embed"
	"encoding/binary"
	"image/color"
	"math"
	"sync"
)

//go:embed fonts/NotoSansMono-Regular.ttf
var fontRegularData []byte

//go:embed fonts/NotoSansMono-Bold.ttf
var fontBoldData []byte

// ttFont is one parsed font: the table offsets and a cache of the outlines read so far.
type ttFont struct {
	data       []byte
	upem       float64
	numGlyphs  int
	locaLong   bool
	loca, glyf []byte
	hmtx       []byte
	numHM      int
	cmap       []byte // the format 4 subtable
	mu         sync.Mutex
	outlines   map[int][][]ttPoint
}

type ttPoint struct {
	x, y float64
	on   bool
}

func parseTTF(data []byte) *ttFont {
	u16 := func(b []byte, o int) int {
		if o < 0 || o+2 > len(b) {
			return 0
		}
		return int(binary.BigEndian.Uint16(b[o:]))
	}
	u32 := func(b []byte, o int) int {
		if o < 0 || o+4 > len(b) {
			return 0
		}
		return int(binary.BigEndian.Uint32(b[o:]))
	}
	tables := map[string][]byte{}
	for i, n := 0, u16(data, 4); i < n; i++ {
		rec := 12 + i*16
		if rec+16 > len(data) {
			break
		}
		off, ln := u32(data, rec+8), u32(data, rec+12)
		if off >= 0 && ln >= 0 && off+ln <= len(data) {
			tables[string(data[rec:rec+4])] = data[off : off+ln]
		}
	}
	head, maxp, hhea := tables["head"], tables["maxp"], tables["hhea"]
	f := &ttFont{data: data, outlines: map[int][][]ttPoint{}}
	f.upem = float64(u16(head, 18))
	f.locaLong = u16(head, 50) == 1
	f.numGlyphs = u16(maxp, 4)
	f.numHM = u16(hhea, 34)
	f.loca, f.glyf, f.hmtx = tables["loca"], tables["glyf"], tables["hmtx"]
	cm := tables["cmap"]
	for i, n := 0, u16(cm, 2); i < n; i++ {
		plat, enc, off := u16(cm, 4+i*8), u16(cm, 6+i*8), u32(cm, 8+i*8)
		if off < len(cm) && u16(cm, off) == 4 && ((plat == 3 && enc == 1) || plat == 0) {
			f.cmap = cm[off:]
			if plat == 3 {
				break
			}
		}
	}
	if f.upem == 0 || len(f.glyf) == 0 || len(f.loca) == 0 || f.cmap == nil {
		return nil
	}
	return f
}

// gid is the glyph for a rune, or 0 (.notdef) when the font has none.
func (f *ttFont) gid(r rune) int {
	if r < 0 || r > 0xFFFF {
		return 0
	}
	c := f.cmap
	u16 := func(o int) int {
		if o < 0 || o+2 > len(c) {
			return 0
		}
		return int(binary.BigEndian.Uint16(c[o:]))
	}
	segX2 := u16(6)
	endO, startO := 14, 16+segX2
	deltaO, rangeO := 16+2*segX2, 16+3*segX2
	for s := 0; s < segX2/2; s++ {
		if int(r) > u16(endO+2*s) {
			continue
		}
		if int(r) < u16(startO+2*s) {
			return 0
		}
		ro := u16(rangeO + 2*s)
		if ro == 0 {
			return (int(r) + u16(deltaO+2*s)) & 0xFFFF
		}
		g := u16(rangeO + 2*s + ro + 2*(int(r)-u16(startO+2*s)))
		if g == 0 {
			return 0
		}
		return (g + u16(deltaO+2*s)) & 0xFFFF
	}
	return 0
}

// advance is a glyph's advance width in font units.
func (f *ttFont) advance(gid int) float64 {
	if f.numHM == 0 {
		return f.upem / 2
	}
	if gid >= f.numHM {
		gid = f.numHM - 1
	}
	if gid*4+2 > len(f.hmtx) {
		return f.upem / 2
	}
	return float64(binary.BigEndian.Uint16(f.hmtx[gid*4:]))
}

// outline is a glyph's contours in font units, y up. Composite glyphs (an accented letter) are
// flattened into their parts.
func (f *ttFont) outline(gid, depth int) [][]ttPoint {
	f.mu.Lock()
	if o, ok := f.outlines[gid]; ok {
		f.mu.Unlock()
		return o
	}
	f.mu.Unlock()
	o := f.readOutline(gid, depth)
	f.mu.Lock()
	f.outlines[gid] = o
	f.mu.Unlock()
	return o
}

func (f *ttFont) glyphBytes(gid int) []byte {
	if gid < 0 || gid >= f.numGlyphs {
		return nil
	}
	var a, b int
	if f.locaLong {
		if gid*4+8 > len(f.loca) {
			return nil
		}
		a, b = int(binary.BigEndian.Uint32(f.loca[gid*4:])), int(binary.BigEndian.Uint32(f.loca[gid*4+4:]))
	} else {
		if gid*2+4 > len(f.loca) {
			return nil
		}
		a, b = 2*int(binary.BigEndian.Uint16(f.loca[gid*2:])), 2*int(binary.BigEndian.Uint16(f.loca[gid*2+2:]))
	}
	if a >= b || b > len(f.glyf) {
		return nil
	}
	return f.glyf[a:b]
}

func (f *ttFont) readOutline(gid, depth int) [][]ttPoint {
	g := f.glyphBytes(gid)
	if len(g) < 10 || depth > 4 {
		return nil
	}
	i16 := func(o int) int { return int(int16(binary.BigEndian.Uint16(g[o:]))) }
	n := i16(0)
	if n >= 0 {
		return f.simpleGlyph(g, n)
	}
	// A composite: each component is another glyph, moved (and maybe scaled) into place.
	var out [][]ttPoint
	o := 10
	for {
		if o+4 > len(g) {
			return out
		}
		flags, comp := int(binary.BigEndian.Uint16(g[o:])), int(binary.BigEndian.Uint16(g[o+2:]))
		o += 4
		var dx, dy float64
		if flags&1 != 0 { // 16-bit arguments
			if o+4 > len(g) {
				return out
			}
			if flags&2 != 0 {
				dx, dy = float64(i16(o)), float64(i16(o+2))
			}
			o += 4
		} else {
			if o+2 > len(g) {
				return out
			}
			if flags&2 != 0 {
				dx, dy = float64(int8(g[o])), float64(int8(g[o+1]))
			}
			o += 2
		}
		a, b, c, d := 1.0, 0.0, 0.0, 1.0
		f2 := func(o int) float64 { return float64(i16(o)) / 16384 }
		switch {
		case flags&0x8 != 0 && o+2 <= len(g):
			a = f2(o)
			d = a
			o += 2
		case flags&0x40 != 0 && o+4 <= len(g):
			a, d = f2(o), f2(o+2)
			o += 4
		case flags&0x80 != 0 && o+8 <= len(g):
			a, b, c, d = f2(o), f2(o+2), f2(o+4), f2(o+6)
			o += 8
		}
		if flags&2 != 0 { // positioned by offset; point-matching components are left out
			for _, ct := range f.outline(comp, depth+1) {
				moved := make([]ttPoint, len(ct))
				for k, p := range ct {
					moved[k] = ttPoint{p.x*a + p.y*c + dx, p.x*b + p.y*d + dy, p.on}
				}
				out = append(out, moved)
			}
		}
		if flags&0x20 == 0 {
			return out
		}
	}
}

func (f *ttFont) simpleGlyph(g []byte, n int) [][]ttPoint {
	if n == 0 {
		return nil
	}
	o := 10
	if o+2*n+2 > len(g) {
		return nil
	}
	ends := make([]int, n)
	for i := range ends {
		ends[i] = int(binary.BigEndian.Uint16(g[o+2*i:]))
	}
	o += 2 * n
	np := ends[n-1] + 1
	o += 2 + int(binary.BigEndian.Uint16(g[o:])) // the instructions are not run
	flags := make([]byte, 0, np)
	for len(flags) < np {
		if o >= len(g) {
			return nil
		}
		fl := g[o]
		o++
		flags = append(flags, fl)
		if fl&8 != 0 {
			if o >= len(g) {
				return nil
			}
			for r := int(g[o]); r > 0 && len(flags) < np; r-- {
				flags = append(flags, fl)
			}
			o++
		}
	}
	coord := func(short, same byte) ([]float64, bool) {
		out := make([]float64, np)
		v := 0
		for i, fl := range flags {
			switch {
			case fl&short != 0:
				if o >= len(g) {
					return nil, false
				}
				d := int(g[o])
				o++
				if fl&same == 0 {
					d = -d
				}
				v += d
			case fl&same == 0:
				if o+2 > len(g) {
					return nil, false
				}
				v += int(int16(binary.BigEndian.Uint16(g[o:])))
				o += 2
			}
			out[i] = float64(v)
		}
		return out, true
	}
	xs, ok := coord(2, 0x10)
	if !ok {
		return nil
	}
	ys, ok := coord(4, 0x20)
	if !ok {
		return nil
	}
	var out [][]ttPoint
	start := 0
	for _, e := range ends {
		if e < start || e >= np {
			return nil
		}
		ct := make([]ttPoint, 0, e-start+1)
		for i := start; i <= e; i++ {
			ct = append(ct, ttPoint{xs[i], ys[i], flags[i]&1 != 0})
		}
		out = append(out, ct)
		start = e + 1
	}
	return out
}

// polyline flattens a contour of on- and off-curve points (quadratic splines) into straight
// segments, in pixels, y down, translated to (ox, oy).
func polyline(ct []ttPoint, k, ox, oy float64) []ttPoint {
	n := len(ct)
	if n < 2 {
		return nil
	}
	pt := func(p ttPoint) ttPoint { return ttPoint{ox + p.x*k, oy - p.y*k, p.on} }
	// Start on a point that is on the curve: the first one, else the middle of the first two off points.
	var seq []ttPoint
	first := -1
	for i, p := range ct {
		if p.on {
			first = i
			break
		}
	}
	if first >= 0 {
		for i := 0; i < n; i++ {
			seq = append(seq, pt(ct[(first+i)%n]))
		}
	} else {
		a, b := pt(ct[0]), pt(ct[1])
		seq = append(seq, ttPoint{(a.x + b.x) / 2, (a.y + b.y) / 2, true})
		for i := 1; i <= n; i++ {
			seq = append(seq, pt(ct[i%n]))
		}
	}
	seq = append(seq, seq[0]) // closed
	var out []ttPoint
	cur := seq[0]
	out = append(out, cur)
	for i := 1; i < len(seq); {
		p := seq[i]
		if p.on {
			out = append(out, p)
			cur = p
			i++
			continue
		}
		// An off-curve control point: the curve ends on the next on-point, or halfway to the next off one.
		var end ttPoint
		next := seq[(i+1)%len(seq)]
		step := 1
		if i+1 < len(seq) && next.on {
			end = next
			step = 2
		} else if i+1 < len(seq) {
			end = ttPoint{(p.x + next.x) / 2, (p.y + next.y) / 2, true}
		} else {
			end = seq[0]
		}
		steps := int(math.Ceil(math.Hypot(p.x-cur.x, p.y-cur.y)+math.Hypot(end.x-p.x, end.y-p.y)) / 2)
		steps = max(4, min(steps, 24))
		for s := 1; s <= steps; s++ {
			t := float64(s) / float64(steps)
			u := 1 - t
			out = append(out, ttPoint{u*u*cur.x + 2*u*t*p.x + t*t*end.x, u*u*cur.y + 2*u*t*p.y + t*t*end.y, true})
		}
		cur = end
		i += step
	}
	return out
}

// coverage is a glyph's anti-aliased alpha mask, rasterised by signed area accumulation: every edge adds
// the area it covers to the cells it crosses, and a running sum along the row is the coverage.
type coverage struct {
	w, h, stride int
	a            []float32
}

// stride is two wider than the glyph: an edge on the last column also writes the one after it.
func newCoverage(w, h int) *coverage {
	return &coverage{w: w, h: h, stride: w + 2, a: make([]float32, (w+2)*h)}
}

func (c *coverage) line(x0, y0, x1, y1 float64) {
	if y0 == y1 {
		return
	}
	dir := float32(1)
	if y0 > y1 {
		dir = -1
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	dxdy := (x1 - x0) / (y1 - y0)
	x := x0
	if y0 < 0 {
		x -= y0 * dxdy
		y0 = 0
	}
	for y := int(y0); y < c.h && float64(y) < y1; y++ {
		dy := math.Min(float64(y+1), y1) - math.Max(float64(y), y0)
		xn := x + dxdy*dy
		d := float32(dy) * dir
		lo, hi := math.Min(x, xn), math.Max(x, xn)
		lo = math.Max(0, math.Min(lo, float64(c.w-1)))
		hi = math.Max(0, math.Min(hi, float64(c.w-1)))
		row := y * c.stride
		fl := math.Floor(lo)
		xi := int(fl)
		xc := int(math.Ceil(hi))
		if xc <= xi+1 {
			xm := float32(0.5*(lo+hi) - fl)
			c.a[row+xi] += d - d*xm
			c.a[row+xi+1] += d * xm
		} else {
			s := float32(1 / (hi - lo))
			x0f := float32(lo - fl)
			a0 := 0.5 * s * (1 - x0f) * (1 - x0f)
			x1f := float32(hi) - float32(xc) + 1
			am := 0.5 * s * x1f * x1f
			c.a[row+xi] += d * a0
			if xc == xi+2 {
				c.a[row+xi+1] += d * (1 - a0 - am)
			} else {
				a1 := s * (1.5 - x0f)
				c.a[row+xi+1] += d * (a1 - a0)
				for k := xi + 2; k < xc-1; k++ {
					c.a[row+k] += d * s
				}
				a2 := a1 + float32(xc-xi-3)*s
				c.a[row+xc-1] += d * (1 - a2 - am)
			}
			c.a[row+xc] += d * am
		}
		x = xn
	}
}

// exportFace picks the weight: the large type of the pictures (titles, the big numbers) is bold.
func exportFace(bold bool) *ttFont {
	faceOnce.Do(func() {
		faceRegular, faceBold = parseTTF(fontRegularData), parseTTF(fontBoldData)
	})
	if bold && faceBold != nil {
		return faceBold
	}
	return faceRegular
}

var (
	faceOnce              sync.Once
	faceRegular, faceBold *ttFont
)

// textAdvance is the width of s in device pixels at em pixels per em.
func (f *ttFont) textAdvance(s string, em float64) float64 {
	w := 0.0
	for _, r := range s {
		w += f.advance(f.glyphFor(r)) * em / f.upem
	}
	return w
}

// glyphFor is the glyph for r, a question mark when the font lacks it.
func (f *ttFont) glyphFor(r rune) int {
	if g := f.gid(r); g != 0 {
		return g
	}
	if r == ' ' || r == ' ' {
		return f.gid(' ')
	}
	return f.gid('?')
}

// drawText blends s onto img with its baseline at (x, y) device pixels.
func (f *ttFont) drawText(dst *canvas, x, y, em float64, s string, col color.RGBA) {
	k := em / f.upem
	for _, r := range s {
		g := f.glyphFor(r)
		f.drawGlyph(dst, g, x, y, k, col)
		x += f.advance(g) * k
	}
}

func (f *ttFont) drawGlyph(dst *canvas, g int, x, y, k float64, col color.RGBA) {
	cts := f.outline(g, 0)
	if len(cts) == 0 {
		return
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, ct := range cts {
		for _, p := range ct {
			minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
			minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
		}
	}
	// The mask's pixel grid starts at the whole pixel left of and above the glyph's box.
	left := math.Floor(x + minX*k)
	top := math.Floor(y - maxY*k)
	w := int(math.Ceil(x+maxX*k)-left) + 1
	h := int(math.Ceil(y-minY*k)-top) + 1
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return
	}
	cv := newCoverage(w, h)
	for _, ct := range cts {
		pl := polyline(ct, k, x-left, y-top)
		for i := 0; i+1 < len(pl); i++ {
			cv.line(pl[i].x, pl[i].y, pl[i+1].x, pl[i+1].y)
		}
	}
	for row := 0; row < h; row++ {
		acc := float32(0)
		for col0 := 0; col0 < w; col0++ {
			acc += cv.a[row*cv.stride+col0]
			a := acc
			if a < 0 {
				a = -a
			}
			if a > 1 {
				a = 1
			}
			if a > 0.004 {
				dst.over(int(left)+col0, int(top)+row, col, a)
			}
		}
	}
}
