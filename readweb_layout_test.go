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
	if !strings.Contains(readerReadCSS, ".rd-text{max-width:48rem;") {
		t.Error("the text column is not 48rem")
	}
}
