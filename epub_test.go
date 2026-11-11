package main

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeEpub writes a small EPUB: two chapters, one image, a navigation file naming the chapters.
func makeEpub(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata><dc:title>A Small Book</dc:title></metadata>
<manifest><item id="n" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="c1" href="text/one.xhtml" media-type="application/xhtml+xml"/><item id="c2" href="text/two.xhtml" media-type="application/xhtml+xml"/><item id="im" href="img/pic.png" media-type="image/png"/><item id="ev" href="img/evil.svg" media-type="image/svg+xml"/></manifest>
<spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`)
	add("OEBPS/nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><nav><ol><li><a href="text/one.xhtml">The Start</a></li><li><a href="text/two.xhtml#x">The End</a></li></ol></nav></body></html>`)
	add("OEBPS/text/one.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title><style>p{color:red}</style><script>alert(1)</script></head>
<body><p id="a" onclick="steal()" style="color:red" class="x">Hello &amp; <em>welcome</em>&nbsp;friend.</p>
<script>alert(2)</script><p><a href="two.xhtml#x">next</a> <a href="javascript:alert(3)">bad</a> <a href="https://example.org/x">web</a> <a href="../nothere.xhtml">gone</a></p>
<p><img src="../img/pic.png" alt="pic" onerror="x()"/><img src="../img/evil.svg"/><img src="https://tracker.example/p.gif"/></p><iframe src="https://x.example"></iframe><form><input name="q"/></form></body></html>`)
	add("OEBPS/text/two.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><div><h2 id="x">Two</h2><p>The end.</p></div></body></html>`)
	add("OEBPS/img/pic.png", "\x89PNG")
	add("OEBPS/img/evil.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(4)</script></svg>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestAnEpubChapterIsSafeHTMLWithItsLinksAndImagesRewritten(t *testing.T) {
	b, err := openEpub(makeEpub(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Title != "A Small Book" || len(b.spine) != 2 || b.chapterTitle(0) != "The Start" || b.chapterTitle(1) != "The End" {
		t.Fatalf("title %q spine %d titles %q %q", b.Title, len(b.spine), b.chapterTitle(0), b.chapterTitle(1))
	}
	got, err := b.chapterHTML(0,
		func(ch int, frag string) string { return fmt.Sprintf("/r?n=%d#%s", ch, frag) },
		func(zp string) string { return "/res?p=" + zp })
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"alert", "onclick", "onerror", "style", "script", "iframe", "<form", "<input", "javascript:", "tracker.example", "nothere", "red", "class="} {
		if strings.Contains(got, bad) {
			t.Errorf("the chapter still carries %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{`<p id="a">Hello &amp; <em>welcome</em>`, `href="/r?n=1#x"`, `href="https://example.org/x" target="_blank" rel="noopener noreferrer"`,
		`src="/res?p=OEBPS/img/pic.png"`, `alt="pic"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the chapter lacks %q:\n%s", want, got)
		}
	}
	// A tag opened in the file is closed in the output, so the page around it stays whole.
	if strings.Count(got, "<p") != strings.Count(got, "</p>") {
		t.Errorf("unbalanced paragraphs:\n%s", got)
	}
	if _, err := b.chapterHTML(5, nil, nil); err == nil {
		t.Error("a chapter past the end was served")
	}
}

func TestAPositionInTheBookAndAPercentageConvertBothWays(t *testing.T) {
	b, err := openEpub(makeEpub(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.percent(0, 0) != 0 || math.Abs(b.percent(1, 1)-100) > 1e-9 {
		t.Errorf("ends: %v %v", b.percent(0, 0), b.percent(1, 1))
	}
	for _, c := range []struct {
		n    int
		frac float64
	}{{0, 0.25}, {0, 0.9}, {1, 0.5}} {
		n, f := b.locate(b.percent(c.n, c.frac))
		if n != c.n || math.Abs(f-c.frac) > 1e-6 {
			t.Errorf("(%d, %v) came back as (%d, %v)", c.n, c.frac, n, f)
		}
	}
}

func TestPartialMD5NamesAFileTheWayKOReaderDoes(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small")
	_ = os.WriteFile(small, []byte("hello"), 0o600)
	if got, _ := koPartialMD5(small); got != fmt.Sprintf("%x", md5.Sum([]byte("hello"))) {
		t.Errorf("a file under 1 KiB is hashed whole: %s", got)
	}
	// Bytes outside the sampled windows do not change the name; bytes inside them do.
	data := make([]byte, 20000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	a, b2, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	_ = os.WriteFile(a, data, 0o600)
	data[3000] ^= 0xff // between the 1 KiB and 4 KiB windows: not sampled
	_ = os.WriteFile(b2, data, 0o600)
	data[3000] ^= 0xff
	data[4096] ^= 0xff // the start of the third window
	_ = os.WriteFile(c, data, 0o600)
	ha, _ := koPartialMD5(a)
	hb, _ := koPartialMD5(b2)
	hc, _ := koPartialMD5(c)
	if ha != hb || ha == hc {
		t.Errorf("sampling is wrong: %s %s %s", ha, hb, hc)
	}
}

func TestProgressIsNewestWinsAndHighlightsReachTheEventStore(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	me := "m@zm.is"
	tk := tenant(me)
	book := strings.Repeat("a", 32)
	if ok, _ := a.savePosition(ctx, tk, Progress{ID: book, Percent: 40, Device: "Kobo", At: 100}, false); !ok {
		t.Fatal("the first position was not stored")
	}
	if ok, _ := a.savePosition(ctx, tk, Progress{ID: book, Percent: 10, Device: "Kobo", At: 90}, false); ok {
		t.Error("an older position replaced a newer one")
	}
	res := a.handleProgressSet(ctx, me, tk, map[string]any{"book": book, "percent": 55.0, "chapter": 3.0, "frac": 0.5})
	if res["found"] != true || res["percent"] != 55.0 || res["device"] != webDeviceName {
		t.Fatalf("set = %v", res)
	}
	if got := a.handleProgressGet(ctx, me, tk, map[string]any{"book": book}); got["percent"] != 55.0 {
		t.Errorf("get = %v", got)
	}
	if got := a.handleProgressGet(ctx, me, tenant("other@zm.is"), map[string]any{"book": book}); got["found"] != false {
		t.Errorf("another person sees the position: %v", got)
	}
	if res := a.handleHighlightAdd(ctx, me, tk, map[string]any{"book": book, "text": "A line worth keeping", "title": "T", "chapter": "One"}); res["ok"] != true {
		t.Fatalf("highlight = %v", res)
	}
	if res := a.handleHighlightAdd(ctx, me, tk, map[string]any{"book": book, "text": "A line worth keeping", "title": "T", "chapter": "One"}); res["ok"] != true {
		t.Fatalf("highlight again = %v", res)
	}
	evs, _ := a.events.List(ctx, tk)
	if len(evs) != 1 || evs[0].Kind != KindHighlight || evs[0].BookMD5 != book || evs[0].DeviceID != webDeviceID {
		t.Errorf("events = %+v", evs)
	}
	if res := a.handleHighlightAdd(ctx, me, tk, map[string]any{"book": "short", "text": "x"}); res["error"] == nil {
		t.Error("a highlight for a malformed book id was saved")
	}
	agent := map[string]any{"_from": "agent.zm.is", "book": book, "text": "x"}
	for name, res := range map[string]map[string]any{
		"get": a.handleProgressGet(ctx, me, tk, agent), "set": a.handleProgressSet(ctx, me, tk, agent), "highlight": a.handleHighlightAdd(ctx, me, tk, agent),
	} {
		if res["error"] != ui.ErrAgentOnlyYou {
			t.Errorf("%s by an agent = %v", name, res)
		}
	}
}

func TestADeviceIsToldOfANewerPositionMadeElsewhereAndNeverOverwritesIt(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("m@zm.is")
	book := strings.Repeat("b", 32)
	kobo := Device{ID: "d1", Name: "Kobo"}
	// The device syncs a position it moved to.
	if got := a.syncPosition(ctx, tk, kobo, &devicePosition{Book: book, Percent: 30, At: 100, Changed: true}); got != nil {
		t.Errorf("told of a position it had just sent: %+v", got)
	}
	// The person reads further in the browser.
	_, _ = a.savePosition(ctx, tk, Progress{ID: book, Percent: 60, Chapter: 4, Device: webDeviceName, At: 200}, true)
	// The device opens the book without moving: it is told, and the browser position stands.
	got := a.syncPosition(ctx, tk, kobo, &devicePosition{Book: book, Percent: 30, At: 100})
	if got == nil || got.Percent != 60 || got.Device != webDeviceName || got.At != 200 {
		t.Fatalf("not told of the browser position: %+v", got)
	}
	if p := a.loadProgress(ctx, tk, book); p.Percent != 60 {
		t.Errorf("opening the book overwrote the browser position: %v", p.Percent)
	}
	// Once the device is at that place it hears nothing more.
	if got := a.syncPosition(ctx, tk, kobo, &devicePosition{Book: book, Percent: 60, At: 200}); got != nil {
		t.Errorf("told again: %+v", got)
	}
	// A move on the device made later than the browser one wins.
	if got := a.syncPosition(ctx, tk, kobo, &devicePosition{Book: book, Percent: 75, At: 300, Changed: true}); got != nil {
		t.Errorf("told of its own move: %+v", got)
	}
	if p := a.loadProgress(ctx, tk, book); p.Percent != 75 || p.Device != "Kobo" {
		t.Errorf("the later device position was not kept: %+v", p)
	}
	if a.syncPosition(ctx, tk, kobo, nil) != nil || a.syncPosition(ctx, tk, kobo, &devicePosition{Book: "x"}) != nil {
		t.Error("a missing or malformed position got an answer")
	}
}
