package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Owner 26856ccd (20:53, 16 Nov): a PDF page "is too big it needs to fit -- its also upscaled".
// The page was fitted to the window's width, so at 1916x937 an A4 or Letter page ran far below
// the fold and a scan was drawn far past its own size. A page now fits whole: its height is at
// most the reading area's, its width at most the area's, and its canvas has at least CSS px × dpr.
func TestAPDFPageFitsWholeAndIsDrawnAtDevicePixels(t *testing.T) {
	if !strings.Contains(readerPDFJS, "rdFit(aw,ah,v0.width,v0.height)") || !strings.Contains(readerPDFJS, "rdCanvas(vp.width,vp.height,r)") {
		t.Fatal("the page is not drawn through rdFit and rdCanvas")
	}
	// The area shrinks after the first draw when the Contents list appears (49px on the owner's
	// book), so the page is redrawn when the area changes, not only when the window does.
	if !strings.Contains(readerPDFJS, "new ResizeObserver(") || !strings.Contains(readerPDFJS, ".observe(t)") ||
		!strings.Contains(readerPDFJS, "drawnW=aw;drawnH=ah;") {
		t.Error("the page is not redrawn when the reading area changes size")
	}
	if strings.Contains(readerPDFJS, "sc=w/v0.width") {
		t.Error("the page is still fitted to the width alone")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on this host")
	}
	// Reading areas: the owner's 1916x937 window (the area under the head and bar is about
	// 1802x760), a phone, and a narrow tall window; pages: A4, Letter, a landscape slide.
	harness := readerPDFFitJS + `
var out=[];[[1802,760],[360,600],[700,1400]].forEach(function(a){[[595,842],[612,792],[960,540]].forEach(function(p){
[1,2,1.5].forEach(function(r){var s=rdFit(a[0],a[1],p[0],p[1]),c=rdCanvas(p[0]*s,p[1]*s,r);
out.push({aw:a[0],ah:a[1],pw:p[0],ph:p[1],r:r,cw:c.cw,ch:c.ch,bw:c.pw,bh:c.ph});});});});
console.log(JSON.stringify(out));`
	raw, err := exec.Command(node, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v %s", err, raw)
	}
	var cases []struct{ Aw, Ah, Pw, Ph, R, Cw, Ch, Bw, Bh float64 }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	for _, c := range cases {
		if c.Ch > c.Ah || c.Cw > c.Aw {
			t.Errorf("page %vx%v in %vx%v is drawn %vx%v: larger than the area", c.Pw, c.Ph, c.Aw, c.Ah, c.Cw, c.Ch)
		}
		if c.Ch < c.Ah-1 && c.Cw < c.Aw-1 {
			t.Errorf("page %vx%v in %vx%v is drawn %vx%v: neither side fills the area", c.Pw, c.Ph, c.Aw, c.Ah, c.Cw, c.Ch)
		}
		if c.Bw < c.Cw*c.R || c.Bh < c.Ch*c.R {
			t.Errorf("at dpr %v a %vx%v CSS page has a %vx%v bitmap: stretched", c.R, c.Cw, c.Ch, c.Bw, c.Bh)
		}
	}
	if len(cases) != 27 {
		t.Errorf("ran %d cases", len(cases))
	}
}
