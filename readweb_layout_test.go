package main

import (
	"strings"
	"testing"
)

// Owner, 26856ccd 7ae0706f: "make use of the space a bit better", with the site footer selected.
// The reader is fitted from its own top to the window's bottom, not shrunk so the footer fits too,
// in the EPUB and the PDF reader alike; and the text column is wider.
func TestTheReaderFillsTheWindowAndTheFooterIsBelowIt(t *testing.T) {
	for name, js := range map[string]string{"epub": readerReadJS, "pdf": readerPDFJS} {
		if strings.Contains(js, "top+below") || !strings.Contains(js, "s.style.setProperty('--rd-chrome',Math.max(top,0)+'px')") {
			t.Errorf("%s: the reader is fitted around what is below it", name)
		}
	}
	if !strings.Contains(readerReadCSS, ".rd-text{max-width:70ch;") {
		t.Error("the text column is not 70ch (about 75 characters a line)")
	}
}

// Owner, 16 Nov: "pdfs should do a better job of prioritising the content". On a wide window the
// reader leaves the site's 56rem centre column, keeping a margin; a PDF page takes that width, and
// an EPUB's text keeps its 70ch measure inside it.
func TestTheReaderLeavesTheCentreColumnOnAWideWindow(t *testing.T) {
	if !strings.Contains(readerReadCSS, "main:has(>.rd-shell){max-width:none}") || !strings.Contains(readerReadCSS, "main:has(>.rd-shell){padding-inline:var(--sp-6)}") {
		t.Error("the reader is still held to the centre column")
	}
	if !strings.Contains(readerPDFCSS, ".rd-text.rd-pdf{max-width:none;") {
		t.Error("the PDF page is capped short of the reader's width")
	}
	if strings.Contains(readerPDFCSS, "60rem") {
		t.Error("the PDF keeps a fixed cap")
	}
	// Web's proof of 05d5422: the page was fitted before its own scrollbar came, so it ran 15px
	// wide and drew a sideways scrollbar. The gutter is kept, so the width the fit reads is final.
	if !strings.Contains(readerReadCSS, "overflow-y:auto;scrollbar-gutter:stable;") {
		t.Error("the reader's scrollbar gutter is not reserved")
	}
	// The bar and Contents stay in the header's 56rem column; only the page itself is wide.
	if !strings.Contains(readerReadCSS, ".rd-shell>.rd-bar,.rd-shell>.rd-toc{width:100%;max-width:56rem;margin-inline:auto;") {
		t.Error("the reader's bar is not in the header's column")
	}
}
