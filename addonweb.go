package main

// addonweb.go — the KOReader plugin, downloadable, with the instructions beside it.
//
// # EMBEDDED, NOT FETCHED
//
// The plugin's source is compiled into this binary with go:embed and zipped per request.
// So the download is always exactly the code that shipped with the running server: there
// is no second artefact to publish, nothing to go stale, and no version of the plugin
// that the server does not know about.
//
// # WHY THE INSTRUCTIONS ARE ON THE SAME PAGE
//
// Installing a KOReader plugin means copying a directory onto a device over USB or
// SSH — there is no store and no installer. Somebody doing that for the first time
// needs the exact path for THEIR device, and the paths differ per platform. A download
// link with no instructions is a puzzle.

import (
	"archive/zip"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// `all:` IS LOAD-BEARING. Without it go:embed silently skips every file whose name
// begins with "_" or "." — and KOReader's plugin descriptor is `_meta.lua`. The archive
// built fine, downloaded fine, unzipped fine, and KOReader would have refused to load the
// plugin with no message at all, because a directory without a _meta.lua is not a plugin.
// Caught by the test that opens the zip and looks for both files; there is no other point
// at which it would have surfaced.
//
//go:embed all:addon
var addonFS embed.FS

const addonDirName = "zmsync.koplugin"

// serveAddonZip streams the plugin as a zip.
//
// The archive contains the PLUGIN DIRECTORY, not its contents loose: KOReader loads
// `plugins/<name>.koplugin/`, and an archive that unpacks bare files into `plugins/`
// produces no plugin and no error — the single most likely way to get this wrong.
func (a *app) serveAddonZip(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.webGate(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+addonDirName+`.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()
	err := fs.WalkDir(addonFS, "addon", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, rerr := addonFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		hdr := &zip.FileHeader{
			// Relative to `addon/`, so the archive root IS zmsync.koplugin/.
			Name:   strings.TrimPrefix(p, "addon/"),
			Method: zip.Deflate,
		}
		// A FIXED timestamp, not time.Now(). Two downloads of an unchanged plugin
		// should be byte-identical, so anyone can check that what they installed is
		// what is running.
		hdr.Modified = time.Unix(0, 0).UTC()
		f, cerr := zw.CreateHeader(hdr)
		if cerr != nil {
			return cerr
		}
		_, werr := f.Write(body)
		return werr
	})
	if err != nil {
		// Headers are already flushed, so http.Error would append a second body to a
		// half-written zip and produce a corrupt file with a 200. The truncated
		// archive fails its own checksum on the client, which is the honest outcome.
		return
	}
}

// serveAddonFile serves one plugin file as plain text, so the code can be READ before it
// is installed. A plugin asking for a device's reading history should be inspectable
// without unzipping anything.
func (a *app) serveAddonFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.webGate(w, r); !ok {
		return
	}
	name := path.Base(strings.TrimPrefix(r.URL.Path, "/addon/file/"))
	body, err := addonFS.ReadFile("addon/" + addonDirName + "/" + name)
	if err != nil {
		http.Error(w, "no such plugin file", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(body)
}

// serveAddon is the install page.
func (a *app) serveAddon(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	_ = caller
	p := a.page("/addon")
	p.StartFor(w, r, "Install the KOReader plugin")
	fmt.Fprint(w, webui.PageHeader("Install the KOReader plugin",
		"Copy one folder onto your reader, then pair it with a six-character code"))

	// webui.ButtonRow, NOT `%s%s`. Two buttons emitted adjacently are separated by
	// whatever whitespace happens to sit between them in the format string, and here
	// there was none — so they rendered edge to edge, which is exactly what was
	// reported against this page. The gap belongs to the CONTAINER (STYLEGUIDE §3),
	// and ButtonRow's container is .field-row, so it is the same gap every other
	// control row in the mesh takes and changing it changes all of them at once.
	fmt.Fprint(w, webui.ButtonRow(
		webui.IconTextButton("fa-solid fa-download", "Download "+addonDirName+".zip",
			webui.BtnOpts{Kind: "primary", Href: "/addon/" + addonDirName + ".zip"}),
		webui.IconTextButton("fa-solid fa-key", "Pair a device", webui.BtnOpts{Kind: "secondary", Href: "/pair"})))

	fmt.Fprint(w, `<div class="card"><h3>1. Put the folder on your reader</h3>`)
	fmt.Fprint(w, `<p class="muted">Unzip it and copy the whole <code>`+addonDirName+`</code> folder into KOReader's <code>plugins</code> directory. Copy the <em>folder</em>, not the files inside it — KOReader looks for a directory whose name ends in <code>.koplugin</code>, and loose files produce no plugin and no error message.</p>`)
	// webui.SimpleTable, not hand-written table markup. A bare table element has NO
	// overflow container above 640px — BaseCSS only gives it one inside
	// @media(max-width:640px) — so on a desktop it grows past its card. SimpleTable
	// renders .lv-tablewrap > .lv-table.tbl-stack: the wrap scrolls, and listViewJS
	// collapses the rows to label:value cards at ANY width the moment a row would
	// overflow. Both come from the page's ListViewHead(), which is why a.page adds it.
	rows := make([]webui.TableRow, 0, 6)
	for _, row := range [][2]string{
		{"Kobo", "<code>.adds/koreader/plugins/</code> on the internal storage"},
		{"Kindle", "<code>koreader/plugins/</code> on the USB drive"},
		{"PocketBook", "<code>applications/koreader/plugins/</code>"},
		{"reMarkable", "<code>~/.local/share/koreader/plugins/</code> over SSH"},
		{"Android", "<code>koreader/plugins/</code> in the app's storage folder"},
		{"Linux / desktop", "<code>~/.config/koreader/plugins/</code>"},
	} {
		rows = append(rows, webui.TableRow{Cells: []template.HTML{
			webui.Text(row[0]), template.HTML(row[1]),
		}})
	}
	fmt.Fprint(w, webui.SimpleTable([]string{"Device", "Where KOReader lives"}, rows))
	fmt.Fprint(w, `<p class="muted">Then restart KOReader — plugins are loaded once at startup, so it will not appear until you do.</p></div>`)

	fmt.Fprint(w, `<div class="card"><h3>2. Pair it</h3>`)
	fmt.Fprint(w, `<ol><li>On this screen, open <a href="/pair">Pair a device</a> and note the six-character code.</li>`)
	fmt.Fprint(w, `<li>On the reader: <strong>☰ → Tools → Zeitgeist reading sync → Pair this device…</strong></li>`)
	fmt.Fprint(w, `<li>Type the code. It is valid for fifteen minutes and can be used once.</li></ol>`)
	fmt.Fprintf(w, `<p class="muted">The reader will sync straight away, and again whenever it connects to wifi. You can force one from the same menu with <strong>Sync now</strong>.</p></div>`)

	fmt.Fprint(w, `<div class="card"><h3>What it sends</h3>`)
	fmt.Fprint(w, `<p class="muted">KOReader's own reading statistics: which book, which page, when you started and for how long. It opens that database <strong>read-only</strong> and never writes to it.</p>`)
	fmt.Fprint(w, `<p class="muted">It also reports which KOReader version and which statistics schema it found, because that has changed between releases — that is how this server knows it is reading your device correctly rather than silently finding nothing.</p>`)
	fmt.Fprint(w, webui.ButtonRow(
		webui.IconTextButton("fa-regular fa-file-code", "Read main.lua", webui.BtnOpts{Kind: "secondary", Href: "/addon/file/main.lua"}),
		webui.IconTextButton("fa-regular fa-file-code", "Read _meta.lua", webui.BtnOpts{Kind: "secondary", Href: "/addon/file/_meta.lua"})))
	fmt.Fprint(w, `</div>`)
	p.End(w)
}

// page is the chrome BOTH hand-written surfaces share, so neither can drift from the
// other. ListViewHead is what makes webui.SimpleTable a house table rather than an
// unstyled one: it carries .lv-tablewrap/.lv-table.tbl-stack and the script that
// collapses an overflowing row to cards at any width. plugrun serves both assets on
// every web plug, so referencing them here costs no per-plug wiring — and a page that
// draws a table without them is the "styled nowhere" half of the same bug.
func (a *app) page(active string) flowweb.Page {
	return flowweb.Page{
		Brand: koreaderApp.Name, Emoji: koreaderApp.Emoji, Icon: koreaderApp.Icon,
		IconColor: koreaderApp.IconColor,
		CSS:       webui.AppShellCSS,
		HeadHTML:  webui.FontAwesomeHead + webui.ListViewHead(),
		Nav:       nav(active),
	}
}
