package main

// epub.go — reading an EPUB in the browser without a script library: the zip is opened on the
// server, one spine item at a time is turned into safe HTML, and the images it names are served
// from the same file. An EPUB's content documents are XHTML, so the standard XML decoder reads
// them; anything outside a small allow-list of tags and attributes is dropped.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"path"
	"strings"
)

const (
	maxEntryBytes  = 20 << 20 // one file inside the book, uncompressed
	maxSpineLength = 5000
)

type epubItem struct {
	ID, Href, Type, Props string
}

// epubBook is an open EPUB: the zip, the spine in reading order and the weight of each spine
// item, which gives a position in the whole book from a position in one chapter.
type epubBook struct {
	zr     *zip.ReadCloser
	files  map[string]*zip.File
	Title  string
	spine  []epubItem
	items  map[string]epubItem // by zip path
	titles map[string]string   // chapter title by zip path
	cum    []int64             // bytes before each spine item; cum[len(spine)] is the total
}

func openEpub(p string) (*epubBook, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	b := &epubBook{zr: zr, files: map[string]*zip.File{}, items: map[string]epubItem{}, titles: map[string]string{}}
	for _, f := range zr.File {
		b.files[path.Clean(f.Name)] = f
	}
	if err := b.parse(); err != nil {
		zr.Close()
		return nil, err
	}
	return b, nil
}

func (b *epubBook) Close() { b.zr.Close() }

// read returns one file of the book, refusing one that is too large once unpacked.
func (b *epubBook) read(name string) ([]byte, error) {
	f := b.files[path.Clean(name)]
	if f == nil {
		return nil, errors.New("not in the book")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEntryBytes {
		return nil, errors.New("file too large")
	}
	return data, nil
}

func xmlDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	return d
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (b *epubBook) parse() error {
	cont, err := b.read("META-INF/container.xml")
	if err != nil {
		return errors.New("not an EPUB")
	}
	var opfPath string
	for d := xmlDecoder(cont); ; {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "rootfile" && opfPath == "" {
			opfPath = attr(se, "full-path")
		}
	}
	if opfPath == "" {
		return errors.New("no package file")
	}
	opf, err := b.read(opfPath)
	if err != nil {
		return err
	}
	dir := path.Dir(opfPath)
	resolve := func(href string) string {
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		return path.Clean(path.Join(dir, href))
	}
	manifest := map[string]epubItem{}
	var order []string
	var inTitle bool
	var ncxHref, navHref string
	d := xmlDecoder(opf)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "title":
				inTitle = b.Title == ""
			case "item":
				it := epubItem{ID: attr(t, "id"), Href: resolve(attr(t, "href")), Type: attr(t, "media-type"), Props: attr(t, "properties")}
				manifest[it.ID] = it
				b.items[it.Href] = it
				if it.Type == "application/x-dtbncx+xml" {
					ncxHref = it.Href
				}
				if strings.Contains(" "+it.Props+" ", " nav ") {
					navHref = it.Href
				}
			case "itemref":
				order = append(order, attr(t, "idref"))
			}
		case xml.CharData:
			if inTitle {
				b.Title = strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			if t.Name.Local == "title" {
				inTitle = false
			}
		}
	}
	for _, id := range order {
		it, ok := manifest[id]
		if !ok || len(b.spine) >= maxSpineLength {
			continue
		}
		if strings.Contains(it.Type, "html") || strings.Contains(it.Type, "xml") {
			b.spine = append(b.spine, it)
		}
	}
	if len(b.spine) == 0 {
		return errors.New("no chapters")
	}
	b.cum = make([]int64, len(b.spine)+1)
	for i, it := range b.spine {
		var n int64 = 1
		if f := b.files[it.Href]; f != nil {
			n = max(int64(f.UncompressedSize64), 1)
		}
		b.cum[i+1] = b.cum[i] + n
	}
	switch {
	case navHref != "":
		b.titlesFromNav(navHref)
	case ncxHref != "":
		b.titlesFromNCX(ncxHref)
	}
	return nil
}

func (b *epubBook) titlesFromNav(p string) {
	data, err := b.read(p)
	if err != nil {
		return
	}
	dir := path.Dir(p)
	d := xmlDecoder(data)
	var href string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "a" {
				href, text = attr(t, "href"), strings.Builder{}
			}
		case xml.CharData:
			if href != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "a" && href != "" {
				b.noteTitle(dir, href, text.String())
				href = ""
			}
		}
	}
}

func (b *epubBook) titlesFromNCX(p string) {
	data, err := b.read(p)
	if err != nil {
		return
	}
	dir := path.Dir(p)
	d := xmlDecoder(data)
	var label string
	var inText bool
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "navPoint":
				label = ""
			case "text":
				inText = label == ""
			case "content":
				b.noteTitle(dir, attr(t, "src"), label)
			}
		case xml.CharData:
			if inText {
				label += string(t)
			}
		case xml.EndElement:
			if t.Name.Local == "text" {
				inText = false
			}
		}
	}
}

func (b *epubBook) noteTitle(dir, href, text string) {
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href = href[:i]
	}
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	key := path.Clean(path.Join(dir, href))
	text = strings.Join(strings.Fields(text), " ")
	if text != "" && b.titles[key] == "" {
		b.titles[key] = text
	}
}

func (b *epubBook) chapterTitle(i int) string {
	if i < 0 || i >= len(b.spine) {
		return ""
	}
	return b.titles[b.spine[i].Href]
}

// spineIndex finds the spine position of a zip path, or -1.
func (b *epubBook) spineIndex(p string) int {
	for i, it := range b.spine {
		if it.Href == p {
			return i
		}
	}
	return -1
}

// percent is how far into the whole book a spot is: chapter n, a fraction into it.
func (b *epubBook) percent(n int, frac float64) float64 {
	if n < 0 || n >= len(b.spine) {
		return 0
	}
	frac = min(max(frac, 0), 1)
	total := float64(b.cum[len(b.spine)])
	return 100 * (float64(b.cum[n]) + frac*float64(b.cum[n+1]-b.cum[n])) / total
}

// locate is the inverse of percent: the chapter and the fraction into it for a percentage.
func (b *epubBook) locate(pct float64) (int, float64) {
	pct = min(max(pct, 0), 100)
	at := pct / 100 * float64(b.cum[len(b.spine)])
	for i := range b.spine {
		if at < float64(b.cum[i+1]) || i == len(b.spine)-1 {
			span := float64(b.cum[i+1] - b.cum[i])
			return i, min(max((at-float64(b.cum[i]))/span, 0), 1)
		}
	}
	return 0, 0
}

// ── chapter to safe HTML ────────────────────────────────────────────────────

var epubKeep = map[string]bool{
	"p": true, "div": true, "span": true, "a": true, "img": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "ul": true, "ol": true, "li": true, "blockquote": true, "pre": true, "code": true,
	"em": true, "strong": true, "i": true, "b": true, "u": true, "sub": true, "sup": true, "br": true, "hr": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true, "caption": true,
	"figure": true, "figcaption": true, "section": true, "article": true, "aside": true, "cite": true, "q": true,
	"dl": true, "dt": true, "dd": true, "small": true, "abbr": true, "s": true, "del": true, "ins": true, "mark": true,
}

// epubDrop are elements whose content is dropped with them.
var epubDrop = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true, "embed": true, "svg": true, "math": true,
	"audio": true, "video": true, "canvas": true, "form": true, "head": true, "title": true, "template": true,
}

var epubVoid = map[string]bool{"br": true, "hr": true, "img": true}

// chapterHTML turns spine item n into safe HTML. Links to other chapters become reader links,
// links to the web open in a new tab, images come from resURL, and every other link and every
// attribute outside the allow-list is dropped.
func (b *epubBook) chapterHTML(n int, readURL func(chapter int, frag string) string, resURL func(zipPath string) string) (string, error) {
	if n < 0 || n >= len(b.spine) {
		return "", errors.New("no such chapter")
	}
	data, err := b.read(b.spine[n].Href)
	if err != nil {
		return "", err
	}
	dir := path.Dir(b.spine[n].Href)
	zipPath := func(href string) string {
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		return path.Clean(path.Join(dir, href))
	}
	var out strings.Builder
	d := xmlDecoder(data)
	inBody, skip := false, 0
	var open []string // output tags still open, so a dropped wrapper never unbalances the page
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if name == "body" {
				inBody = true
				continue
			}
			if skip > 0 {
				skip++
				continue
			}
			if epubDrop[name] {
				if inBody {
					skip = 1
				}
				continue
			}
			if !inBody || !epubKeep[name] {
				open = append(open, "")
				continue
			}
			out.WriteString("<" + name)
			for _, a := range t.Attr {
				key := strings.ToLower(a.Name.Local)
				switch {
				case key == "id" && a.Name.Space == "":
					fmt.Fprintf(&out, ` id="%s"`, html.EscapeString(a.Value))
				case key == "title" || key == "alt" || key == "colspan" || key == "rowspan" || key == "lang":
					fmt.Fprintf(&out, ` %s="%s"`, key, html.EscapeString(a.Value))
				case name == "a" && key == "href":
					out.WriteString(b.linkAttr(a.Value, zipPath, readURL))
				case name == "img" && key == "src":
					p := zipPath(a.Value)
					if it, ok := b.items[p]; ok && strings.HasPrefix(it.Type, "image/") {
						fmt.Fprintf(&out, ` src="%s"`, html.EscapeString(resURL(p)))
					}
				}
			}
			out.WriteString(">")
			if epubVoid[name] {
				open = append(open, "")
			} else {
				open = append(open, name)
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if name == "body" {
				inBody = false
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			if len(open) == 0 {
				continue
			}
			last := open[len(open)-1]
			open = open[:len(open)-1]
			if last != "" {
				out.WriteString("</" + last + ">")
			}
		case xml.CharData:
			if inBody && skip == 0 {
				out.WriteString(html.EscapeString(string(t)))
			}
		}
	}
	for i := len(open) - 1; i >= 0; i-- {
		if open[i] != "" {
			out.WriteString("</" + open[i] + ">")
		}
	}
	return out.String(), nil
}

func (b *epubBook) linkAttr(href string, zipPath func(string) string, readURL func(int, string) string) string {
	href = strings.TrimSpace(href)
	switch {
	case href == "":
		return ""
	case strings.HasPrefix(href, "#"):
		return fmt.Sprintf(` href="%s"`, html.EscapeString(href))
	case strings.HasPrefix(strings.ToLower(href), "http://"), strings.HasPrefix(strings.ToLower(href), "https://"):
		return fmt.Sprintf(` href="%s" target="_blank" rel="noopener noreferrer" hx-boost="false"`, html.EscapeString(href))
	case strings.Contains(href, ":"):
		return "" // mailto, javascript, data and the rest
	}
	frag := ""
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href, frag = href[:i], href[i+1:]
	}
	if i := b.spineIndex(zipPath(href)); i >= 0 {
		return fmt.Sprintf(` href="%s"`, html.EscapeString(readURL(i, frag)))
	}
	return ""
}
