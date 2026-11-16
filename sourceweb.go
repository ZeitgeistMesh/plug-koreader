package main

// sourceweb.go — /settings (connect a book server) and /books (the saved list of its books).
//
// Every control calls the capabilities' own handlers, so the page cannot accept an address the
// capability would refuse. The password box is write-only: a saved password shows as a
// placeholder, and the page is never given the value.

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// sourceTestBudget is the longest the Test button waits for the server.
const sourceTestBudget = 20 * time.Second

const (
	settingsPath = "/settings"
	booksPath    = "/books"
	readingPath  = "/reading"
	settingsRoot = "reader-settings"
)

const readerBooksCSS = `
.rd-cover{display:block;height:calc(var(--sp-6) + var(--sp-4));width:auto}
.rd-status{margin-block:var(--sp-3)}
.rd-acts .btn-group{max-width:none;margin-left:auto}
.rd-acts{--rd-act-w:5.75rem}
/* Every row button is one width, so the PDF, Read, EPUB and BIN buttons of different rows line up
   edge to edge instead of being 3 to 13 px apart by the length of their label (owner, plan 26856ccd). */
.rd-acts .btn{white-space:nowrap;min-width:var(--rd-act-w)}
.rd-title{overflow-wrap:anywhere}
@media (min-width: 641px){#lv-books td[data-k=formats]{width:1%;white-space:nowrap;text-align:right}}
.rd-form{display:flex;flex-direction:column;gap:var(--sp-3)}
.rd-field{display:flex;flex-direction:column;gap:var(--sp-1)}
.rd-field label{margin:0}
.rd-field .hint{margin:0;font-size:.78rem}
.rd-result{display:flex;align-items:center;gap:var(--sp-2);margin:0;min-height:var(--control-h)}
.rd-result.ok{color:var(--ok,var(--accent))}
.rd-result.bad{color:var(--danger-text)}
.rd-result .rd-msg{display:inline-flex;align-items:center;gap:var(--sp-2)}
.rd-result .rd-wait{display:none}
.rd-result.htmx-request .rd-wait{display:inline}
.rd-result.htmx-request .rd-msg{display:none}
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
	p.CSS += readerBooksCSS
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
		// A test waits on the person's own server, so it is bounded: the answer is always a line on the page.
		ctx, cancel := context.WithTimeout(r.Context(), sourceTestBudget)
		defer cancel()
		res := a.handleSourceTest(ctx, caller, tk, meta)
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
	if r.Method == http.MethodPost && r.FormValue("do") == "test" {
		// A test never changes what is saved, so the form keeps what the person typed.
		url, user = r.FormValue("url"), r.FormValue("username")
	}
	line, state := note, "ok"
	switch {
	case line != "":
		if bad {
			state = "bad"
		}
	case s == nil:
		line, state = ui.StatusNone, ""
	case s.TestError != "":
		line, state = s.TestError, "bad"
	case s.TestedAt != "":
		// A page load shows the last test in words. It used to draw the tick alone, with no sentence.
		line = fill(ui.StatusOK, "server", serverName(map[string]any{"title": s.Title}))
	default:
		state = "" // never tested: nothing to say, so no tick either
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div id="%s">`, settingsRoot)
	field := func(id, label, control, help string) {
		fmt.Fprintf(&b, `<div class="rd-field"><label for="%s">%s</label>%s<p class="hint">%s</p></div>`, id, html.EscapeString(label), control, html.EscapeString(help))
	}
	fmt.Fprintf(&b, `<form class="rd-form" method="post" action="%s" hx-post="%s" hx-target="#%s" hx-select="#%s" hx-swap="outerHTML">`,
		settingsPath, settingsPath, settingsRoot, settingsRoot)
	field("rd-url", ui.LabelURL, fmt.Sprintf(`<input id="rd-url" type="text" name="url" value="%s" maxlength="300" placeholder="%s" autocomplete="off" required>`,
		html.EscapeString(url), html.EscapeString(ui.PlaceholderURL)), ui.HintURL)
	field("rd-user", ui.LabelUser, fmt.Sprintf(`<input id="rd-user" type="text" name="username" value="%s" maxlength="200" autocomplete="off">`,
		html.EscapeString(user)), ui.HintUser)
	field("rd-pass", ui.LabelPassword, fmt.Sprintf(`<input id="rd-pass" type="password" name="password" maxlength="400" autocomplete="new-password" placeholder="%s">`,
		html.EscapeString(passPlaceholder)), ui.HintPassword)
	// Test swaps ONLY the result line under the buttons, so what is typed stays and the answer
	// appears where the finger is. Without scripts it is an ordinary form post.
	testAttrs := fmt.Sprintf(`name="do" value="test" hx-post="%s" hx-include="closest form" hx-vals='{"do":"test"}' hx-target="#rd-result" hx-select="#rd-result" hx-swap="outerHTML" hx-indicator="#rd-result"`, settingsPath)
	btns := []template.HTML{
		webui.IconTextButton("fa-solid fa-plug-circle-check", ui.BtnTest, webui.BtnOpts{Kind: "secondary", Type: "submit", Attrs: testAttrs}),
		webui.IconTextButton("fa-solid fa-floppy-disk", ui.BtnSave, webui.BtnOpts{Kind: "primary", Type: "submit", Attrs: `name="do" value="save"`}),
	}
	if s != nil {
		btns = append(btns, webui.IconTextButton("fa-solid fa-link-slash", ui.BtnDisconnect,
			webui.BtnOpts{Kind: "danger", Type: "submit", Attrs: `name="do" value="delete" formnovalidate`}))
	}
	b.WriteString(string(webui.ButtonRow(btns...)))
	icon := ""
	switch state {
	case "ok":
		icon = `<i class="fa-solid fa-circle-check" aria-hidden="true"></i>`
	case "bad":
		icon = `<i class="fa-solid fa-circle-xmark" aria-hidden="true"></i>`
	}
	fmt.Fprintf(&b, `<p id="rd-result" class="rd-result %s" role="status" aria-live="polite"><span class="rd-wait">%s</span><span class="rd-msg">%s%s</span></p>`,
		state, html.EscapeString(ui.StatusTesting), icon, html.EscapeString(line))
	b.WriteString(`</form></div>`)
	return b.String()
}

// ── /books ──────────────────────────────────────────────────────────────────

func cmpText(a, b string, desc bool) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if desc {
		return a > b
	}
	return a < b
}

func catalogStatus(c *Catalog, refreshing bool) string {
	var parts []string
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
	// One statement, not two: "Updated 20 h ago" with a spinner while a refresh runs. The words "Updating…"
	// only appear when there is no earlier update to report (owner via master, plan 26856ccd).
	spin := ""
	if refreshing {
		spin = `<i class="fa-solid fa-rotate fa-spin" aria-hidden="true"></i> `
		if len(parts) == 0 {
			parts = append(parts, ui.ReadingLine)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return `<p class="hint rd-status" role="status">` + spin + html.EscapeString(strings.Join(parts, " ")) + `</p>`
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
