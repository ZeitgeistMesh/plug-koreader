package main

import (
	"image/color"
	"testing"
)

// The exports use one embedded typeface (owner, dab02a73 e81b1fce: "pick a different font for all
// exports"). It must parse, measure proportionally, draw ink, and cover the accents a title carries.
func TestExportFontParsesAndMeasures(t *testing.T) {
	reg, bold := exportFace(false), exportFace(true)
	if reg == nil || bold == nil {
		t.Fatal("the embedded fonts do not parse")
	}
	if reg == bold {
		t.Error("bold must be its own face")
	}
	if textW("iiii", 2) >= textW("WWWW", 2) {
		t.Error("the type is proportional: four i's are narrower than four W's")
	}
	if textW("Reading 2026", 5) <= textW("Reading 2026", 2) {
		t.Error("a bigger step is wider")
	}
	for _, r := range "aé ÅЖ?" {
		if r != ' ' && reg.gid(r) == 0 && r != '?' {
			t.Errorf("no glyph for %q", r)
		}
	}
}

func TestExportFontDrawsInk(t *testing.T) {
	for _, s := range []string{"Dune", "Émile Zola", "Война"} {
		c := newCanvas(120, 30)
		c.rect(0, 0, 120, 30, imgBG)
		c.text(4, 8, 2, s, imgText)
		ink := 0
		for y := 0; y < c.img.Bounds().Dy(); y++ {
			for x := 0; x < c.img.Bounds().Dx(); x++ {
				if p := c.img.RGBAAt(x, y); p != (color.RGBA{imgBG.R, imgBG.G, imgBG.B, 255}) {
					ink++
				}
			}
		}
		if ink < 150 {
			t.Errorf("%q drew %d pixels", s, ink)
		}
	}
}

// A title that does not fit is cut to what does, with dots; one that fits is left alone.
func TestFitCutsToTheWidth(t *testing.T) {
	long := "The Hitchhiker's Guide to the Galaxy and Everything After"
	if got := fit("Dune", 2, 300); got != "Dune" {
		t.Errorf("a short title changed: %q", got)
	}
	got := fit(long, 2, 200)
	if got == long || textW(got, 2) > 200 || got[len(got)-3:] != "..." {
		t.Errorf("fit = %q (%d wide)", got, textW(got, 2))
	}
}
