package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"
)

// Owner, dab02a73 18901aa3: the pictures are drawn in a mono face, which is wider. Every tile label
// still fits its tile, on the year picture too, where six tiles stand three to a row.
func TestPictureTileLabelsFitInTheMonoFace(t *testing.T) {
	idx, loc := yearFixture()
	for _, tl := range yearTiles(buildYearStats(idx, loc, 2026)) {
		if textW(tl[1], 2) > tileWidth(yearTilesPerRow)-20 {
			t.Errorf("year tile label %q is %d wide, the tile has %d", tl[1], textW(tl[1], 2), tileWidth(yearTilesPerRow)-20)
		}
	}
}

// Nothing is cut at the foot of a picture (the last month's bar on Stats was), and the title's
// descenders never reach the tiles under it: both bands are bare background.
func TestPicturesKeepAMarginAtTheFootAndUnderTheTitle(t *testing.T) {
	idx, loc := yearFixture()
	year, err := yearPNG(idx, loc, 2026, time.Date(2026, 11, 14, 12, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := statsPNG(idx, loc, 2026)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"year": year, "stats": stats} {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		h := img.Bounds().Dy()
		if y := inkRow(img, h-(imgMargin-8)*imgU, h); y >= 0 {
			t.Errorf("%s: ink at pixel row %d of %d, inside the foot margin", name, y, h)
		}
		if y := inkRow(img, (picTilesY-6)*imgU, picTilesY*imgU); y >= 0 {
			t.Errorf("%s: the title reaches pixel row %d, the tiles start at %d", name, y, picTilesY*imgU)
		}
	}
}

// inkRow is the first pixel row in [from, to) with anything but the background on it, or -1.
func inkRow(img image.Image, from, to int) int {
	for y := from; y < to; y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if uint8(r>>8) != imgBG.R || uint8(g>>8) != imgBG.G || uint8(b>>8) != imgBG.B {
				return y
			}
		}
	}
	return -1
}
