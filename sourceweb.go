package main

// sourceweb.go — /settings (connect a book server) and /books (the saved list of its books).
//
// Every control calls the capabilities' own handlers, so the page cannot accept an address the
// capability would refuse. The password box is write-only: a saved password shows as a
// placeholder, and the page is never given the value.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

const (
	settingsPath = "/settings"
	booksPath    = "/books"
	readingPath  = "/reading"
	settingsRoot = "reader-settings"
)

const readerBooksCSS = `
.rd-cover{display:block;height:calc(var(--sp-6) + var(--sp-4));width:auto}
.rd-status{margin-block:var(--sp-3)}
`

// fill replaces {name} markers in a copy string.
func fill(s string, kv ...string) string {
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// ── /settings ───────────────────────────────────────────────────────────────

func (a *app) serveSettings(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	var note string
	var bad bool
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		_ = r.ParseForm()
		note, bad = a.settingsAction(r, caller)
	case http.MethodGet:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if flowweb.WantsFragment(r) && r.Method == http.MethodPost {
		fmt.Fprint(w, a.settingsBody(r, tk, note, bad))
		return
	}
	p := a.page(settingsPath)
	p.StartFor(w, r, ui.SettingsTitle)
	fmt.Fprint(w, a.settingsBody(r, tk, note, bad))
	p.End(w)
}

// settingsAction runs the pressed button and returns the line to show.
func (a *app) settingsAction(r *http.Request, caller string) (string, bool) {
	tk := tenant(caller)
	meta := map[string]any{
		"url": r.FormValue("url"), "username": r.FormValue("username"), "password": r.FormValue("password"),
		"caller_user_id": caller,
	}
	switch r.FormValue("do") {
	case "test":
		res := a.handleSourceTest(r.Context(), caller, tk, meta)
		if e, _ := res["error"].(string); e != "" {
			return e, true
		}
		return fill(ui.StatusOK, "server", serverName(res)), false
	case "save":
		res := a.handleSourceSet(r.Context(), caller, tk, meta)
		if e, _ := res["error"].(string); e != "" {
			return e, true
		}
		t := a.handleSourceTest(r.Context(), caller, tk, map[string]any{"caller_user_id": caller})
		a.refreshSoon(tk)
		if e, _ := t["error"].(string); e != "" {
			return fill(ui.StatusSavedFailed, "problem", e), true
		}
		return fill(ui.StatusSavedOK, "server", serverName(t)), false
	case "delete":
		a.handleSourceDelete(r.Context(), caller, tk, map[string]any{"caller_user_id": caller})
		return ui.StatusDisconnected, false
	}
	return "", false
}

func serverName(res map[string]any) string {
	if t, _ := res["title"].(string); strings.TrimSpace(t) != "" {
		return t
	}
	return "your server"
}

func (a *app) settingsBody(r *http.Request, tk dblib.TenantKey, note string, bad bool) string {
	s := a.loadSource(r.Context(), tk)
	var url, user, passPlaceholder string
	if s != nil {
		url, user = s.URL, s.Username
		if s.Secret != "" {
			passPlaceholder = ui.PlaceholderPasswordSet
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div id="%s">`, settingsRoot)
	line := note
	switch {
	case line != "":
	case s == nil:
		line = ui.StatusNone
	case s.TestError != "":
		line = s.TestError
		bad = true
	}
	if line != "" {
		cls := "hint"
		if bad {
			cls = "hint error"
		}
		fmt.Fprintf(&b, `<p class="%s rd-status" role="status">%s</p>`, cls, html.EscapeString(line))
	}
	field := func(id, label, control, help string) {
		fmt.Fprintf(&b, `<div><label for="%s">%s</label>%s<p class="hint">%s</p></div>`, id, html.EscapeString(label), control, html.EscapeString(help))
	}
	fmt.Fprintf(&b, `<form class="form-grid" method="post" action="%s" hx-post="%s" hx-target="#%s" hx-select="#%s" hx-swap="outerHTML">`,
		settingsPath, settingsPath, settingsRoot, settingsRoot)
	field("rd-url", ui.LabelURL, fmt.Sprintf(`<input id="rd-url" type="text" name="url" value="%s" maxlength="300" placeholder="%s" autocomplete="off" required>`,
		html.EscapeString(url), html.EscapeString(ui.PlaceholderURL)), ui.HintURL)
	field("rd-user", ui.LabelUser, fmt.Sprintf(`<input id="rd-user" type="text" name="username" value="%s" maxlength="200" autocomplete="off">`,
		html.EscapeString(user)), ui.HintUser)
	field("rd-pass", ui.LabelPassword, fmt.Sprintf(`<input id="rd-pass" type="password" name="password" maxlength="400" autocomplete="new-password" placeholder="%s">`,
		html.EscapeString(passPlaceholder)), ui.HintPassword)
	btns := []string{
		string(webui.IconTextButton("fa-solid fa-plug-circle-check", ui.BtnTest, webui.BtnOpts{Kind: "secondary", Type: "submit", Attrs: `name="do" value="test"`})),
		string(webui.IconTextButton("fa-solid fa-floppy-disk", ui.BtnSave, webui.BtnOpts{Kind: "primary", Type: "submit", Attrs: `name="do" value="save"`})),
	}
	if s != nil {
		btns = append(btns, string(webui.IconTextButton("fa-solid fa-link-slash", ui.BtnDisconnect,
			webui.BtnOpts{Kind: "danger", Type: "submit", Attrs: `name="do" value="delete" formnovalidate`})))
	}
	b.WriteString(`<div>`)
	for _, x := range btns {
		b.WriteString(x)
	}
	b.WriteString(`</div></form></div>`)
	return b.String()
}

// ── /books ──────────────────────────────────────────────────────────────────

func catalogListView() webui.ListView {
	return webui.ListView{
		Action:            booksPath,
		RootID:            "lv-catalog",
		Title:             ui.BooksTitle,
		SearchPlaceholder: ui.SearchPlaceholder,
		EmptyText:         ui.EmptyNoneMatch,
		PageSizes:         []int{25, 50, 100},
		Columns: []webui.Column{
			{Key: "cover", Label: ""},
			{Key: "title", Label: ui.ColTitle, Sortable: true},
			{Key: "authors", Label: ui.ColAuthor, Sortable: true, Priority: 1},
			{Key: "formats", Label: ui.ColFormats, Priority: 2},
		},
	}
}

func catalogRows(entries []CatalogEntry, q webui.ListQuery) ([]webui.Row, int) {
	hits := searchCatalog(entries, q.Search)
	sorted := append([]CatalogEntry(nil), hits...)
	less := func(i, j int) bool { return false }
	switch q.Sort {
	case "title":
		less = func(i, j int) bool { return cmpText(sorted[i].Title, sorted[j].Title, q.Desc) }
	case "authors":
		less = func(i, j int) bool { return cmpText(sorted[i].Authors, sorted[j].Authors, q.Desc) }
	}
	sort.SliceStable(sorted, less)
	total := len(sorted)
	lo := min(q.Offset(), total)
	hi := min(lo+q.Limit(), total)
	rows := make([]webui.Row, 0, hi-lo)
	for _, e := range sorted[lo:hi] {
		key := entryKey(e.ID)
		cover := webui.Text("")
		if e.Cover != "" {
			cover = template.HTML(fmt.Sprintf(`<img class="rd-cover" loading="lazy" alt="" src="%s/cover?id=%s">`, booksPath, key))
		}
		var fm []string
		for i, f := range e.Files {
			fm = append(fm, fmt.Sprintf(`<a href="%s/file?id=%s&amp;f=%d" hx-boost="false" data-tip="%s">%s</a>`,
				booksPath, key, i, html.EscapeString(ui.DownloadTitle), html.EscapeString(strings.ToUpper(fileExt(f.Type)))))
		}
		rows = append(rows, webui.Row{
			"cover":   cover,
			"title":   webui.Text(e.Title),
			"authors": webui.Text(e.Authors),
			"formats": template.HTML(strings.Join(fm, " ")),
		})
	}
	return rows, total
}

func cmpText(a, b string, desc bool) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if desc {
		return a > b
	}
	return a < b
}

func (a *app) serveCatalog(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	// A stale or missing list starts a refresh behind the page. The page itself reads only
	// what is saved.
	if catalogDue(c, s, time.Now()) {
		a.refreshSoon(tk)
	}
	view := catalogListView()
	q := view.ParseListQuery(r)
	var entries []CatalogEntry
	if c != nil {
		entries = c.Entries
	}
	rows, total := catalogRows(entries, q)
	if flowweb.WantsFragment(r) && r.Method == http.MethodGet {
		view.RenderFragment(w, q, rows, total)
		return
	}
	p := a.page(booksPath)
	p.CSS += readerBooksCSS
	p.StartFor(w, r, ui.BooksTitle)
	switch {
	case s == nil:
		fmt.Fprintf(w, `<p class="hint rd-status">%s <a href="%s">%s</a>.</p>`, html.EscapeString(ui.EmptyNotConnected), settingsPath, html.EscapeString(ui.NavSettings))
	default:
		fmt.Fprint(w, catalogStatus(c, a.isRefreshing(tk)))
		if total == 0 && c == nil && a.isRefreshing(tk) {
			fmt.Fprintf(w, `<p class="hint">%s</p>`, html.EscapeString(ui.EmptyLoading))
		} else if total == 0 && c != nil && len(c.Entries) == 0 && c.Error == "" {
			fmt.Fprintf(w, `<p class="hint">%s</p>`, html.EscapeString(ui.EmptyNoneAtAll))
		} else {
			view.Render(w, q, rows, total)
		}
	}
	p.End(w)
}

func catalogStatus(c *Catalog, refreshing bool) string {
	var parts []string
	if refreshing {
		parts = append(parts, ui.ReadingLine)
	}
	if c != nil {
		if c.Error != "" {
			parts = append(parts, fill(ui.FailedLine, "problem", c.Error))
		}
		if t, err := time.Parse(time.RFC3339, c.RefreshedAt); err == nil {
			parts = append(parts, fill(ui.UpdatedLine, "when", ago(time.Since(t))))
		}
		if c.Truncated {
			parts = append(parts, fill(ui.TruncatedLine, "n", fmt.Sprint(len(c.Entries))))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return `<p class="hint rd-status" role="status">` + html.EscapeString(strings.Join(parts, " ")) + `</p>`
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
