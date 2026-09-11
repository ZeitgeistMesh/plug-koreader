package main

// pairweb.go — minting a code on a real screen, and claiming it from the reader.
//
// Two surfaces that must not be confused:
//
//   /pair          a BROWSER page, behind the normal SSO gate. Minting a code is what
//                  binds a device to a person, so it can only be done by somebody who
//                  has already proved who they are.
//   /api/pair      the DEVICE endpoint. Unauthenticated by necessity — the reader has
//                  no identity yet, which is the entire point — and it accepts nothing
//                  but a code that was minted moments ago by an authenticated session.
//
// The device endpoint reads no Remote-* header. It cannot: the edge sends it straight
// to the socket with `import stripidentity`, so a forged identity header is inert.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/webui"
)

// servePair shows the device list and mints codes.
func (a *app) servePair(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)

	var minted string
	if r.Method == http.MethodPost {
		switch r.FormValue("action") {
		case "revoke":
			a.revokeDevice(r, tk, strings.TrimSpace(r.FormValue("device")))
		case "delete":
			a.deleteDevice(r.Context(), tk, strings.TrimSpace(r.FormValue("device")))
		default:
			pc, err := newPairCode(tk)
			if err == nil && a.pairCodes.Put(r.Context(), pairCodeTenant(), pc.ID, &pc) == nil {
				minted = pc.ID
			}
		}
	}

	// BEFORE the page chrome. An htmx control swap wants only the #lv-devices block, and
	// StartFor has already written a doctype and a nav by the time the list is reached —
	// so this check has to happen here, not beside the list it belongs to.
	//
	// flowweb.WantsFragment, NOT a bare HX-Request test. The shell's navbar is BOOSTED, so
	// clicking "Devices" is also an htmx request — and answering that with the bare list
	// replaced the whole page with a naked table. WantsFragment is the house predicate and
	// excludes exactly that case (HX-Request AND NOT HX-Boosted); a hand-rolled header
	// check is how the boosted-navigation half gets forgotten.
	if flowweb.WantsFragment(r) && r.Method == http.MethodGet {
		devices, _ := a.devices.List(r.Context(), tk)
		view := a.devicesListView()
		q := view.ParseListQuery(r)
		rows, total := deviceRows(devices, q)
		view.RenderFragment(w, q, rows, total)
		return
	}

	p := a.page("/pair")
	p.StartFor(w, r, "Devices")
	// NO PageHeader. The nav already says Devices and the ListView's own Title says
	// "Paired readers", so a third heading restating both was two lines of chrome above
	// a list that introduces itself.

	if minted != "" {
		fmt.Fprint(w, `<div class="card"><h3>Type this on your reader</h3>`)
		// SPACED IN THE MIDDLE and in a monospace face. It is read off one screen and
		// typed on another, at low contrast, by somebody holding both — so the size and
		// the tracking are the point, not decoration.
		//
		// THE LAST INLINE STYLE IN THIS PLUG, and it is still one: an inline style
		// outranks every stylesheet, so nothing can theme this. The honest fix is a
		// shared component — plug-whatsapp renders the SAME shape for its phone-linking
		// code, also inline-styled, and two plugs is when a shape belongs in webui — but
		// that is a BaseCSS change, and BaseCSS reaches browsers through mod-shell, so it
		// is a fleet release rather than a plug edit. Until then: the spacing at least
		// comes off the shared scale, so only the typography is local.
		fmt.Fprintf(w, `<p style="font-family:var(--font-mono,ui-monospace,monospace);font-size:2.2rem;letter-spacing:.35rem;margin:var(--sp-1,.25rem) 0">%s</p>`,
			html.EscapeString(minted[:3]+" "+minted[3:]))
		fmt.Fprint(w, `<p class="muted">Valid for fifteen minutes, and it can be used once. On the reader: <strong>☰ → Tools → Zeitgeist reading sync → Pair this device…</strong></p>`)
		fmt.Fprintf(w, `<p class="muted">No plugin yet? <a href="/addon">Install it first</a>.</p></div>`)
	}

	devices, _ := a.devices.List(r.Context(), tk)

	// THE HOUSE ListView, not SimpleTable. SimpleTable solved the overflow this page
	// was reported for and nothing else: it is chrome-free, so this list had no search,
	// no pagination, no column switcher and no way to act on more than one row — while
	// every other list in the mesh has all four. A device list grows by one per reader
	// and never shrinks on its own, so "no way to select several and delete them" is the
	// state it ends up in.
	view := a.devicesListView()
	q := view.ParseListQuery(r)
	rows, total := deviceRows(devices, q)

	// RENDERED BARE, no card around it — the house list layout (compare the ideas list at
	// /?status=open). A ListView already draws its own surface: controls row, table wrap,
	// pager. Putting that inside a .card gives the page two nested frames, an inset inside
	// an inset, which is the "extra container" this page was reported for. The card belongs
	// to a block of prose or a form, not to a list that is already a component.
	view.Render(w, q, rows, total)
	fmt.Fprint(w, `<p class="muted">The token is shown only by its last four characters — the rest is stored as a hash and cannot be read back, here or anywhere else.</p>`)
	// One button, so no row container and no margin of its own: the paragraph above it
	// already carries the house paragraph margin. The hand-tuned top margin that used to
	// sit on this form was a literal off the shared scale, which is the drift the style
	// guide's §3 exists to stop — and dynlint's off_scale_spacing counts.
	fmt.Fprintf(w, `<form method="post">%s</form>`,
		webui.IconTextButton("fa-solid fa-key", "Mint a pairing code",
			webui.BtnOpts{Kind: "primary", Type: "submit"}))
	p.End(w)
}

// devicesListView declares the readers list.
//
// The columns are ordered by how often they answer a question, and the two that made the
// original table unreadable carry a Priority so they collapse FIRST on a narrow screen —
// their value is never lost, because any hidden column puts a per-row ⓘ button on the row
// whose modal lists every field. That is the house answer to "this column is long and
// rarely needed", and it is better than the previous one (let it wrap and hope).
func (a *app) devicesListView() webui.ListView {
	return webui.ListView{
		Action:            "/pair",
		RootID:            "lv-devices",
		Title:             "Paired readers",
		SearchPlaceholder: "Search readers…",
		EmptyText:         "None yet. Mint a code below, then type it into the plugin on your reader.",
		PageSizes:         []int{10, 25, 50},
		Selectable:        true,
		BulkActions: []webui.BulkAction{
			{Label: "Delete selected", Path: "/pair/delete", Danger: true, Icon: "fa-solid fa-trash",
				Confirm: "Permanently delete %n selected readers? Each will have to pair again with a new code."},
		},
		Columns: []webui.Column{
			{Key: "reader", Label: "Reader", Sortable: true},
			{Key: "lastsync", Label: "Last sync", Sortable: true},
			{Key: "koreader", Label: "KOReader", Sortable: true, Priority: 1},
			{Key: "token", Label: "Token", Priority: 2},
			// The schema string is the longest value on the page — the thing that was
			// "breaking into lines". Highest priority number, so it is the first to
			// collapse, and it never wraps: see schemaCell.
			{Key: "stats", Label: "Statistics", Sortable: true, Priority: 3},
			{Key: "actions", Label: ""},
		},
	}
}

// deviceRows filters, sorts and pages the readers into ListView rows.
func deviceRows(devices []Device, q webui.ListQuery) ([]webui.Row, int) {
	filtered := make([]Device, 0, len(devices))
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	for _, d := range devices {
		if needle != "" {
			hay := strings.ToLower(strings.Join([]string{
				d.Name, d.KOReaderVersion, d.StatsSchema, d.DeviceModel, d.TokenTail,
			}, " "))
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		filtered = append(filtered, d)
	}

	// Newest first by default — a reader you just paired is the one you are looking at.
	sortKey := func(d Device) string { return d.CreatedAt }
	switch q.Sort {
	case "reader":
		sortKey = func(d Device) string { return strings.ToLower(d.Name) }
	case "lastsync":
		sortKey = func(d Device) string { return d.LastSyncAt }
	case "koreader":
		sortKey = func(d Device) string { return d.KOReaderVersion }
	case "stats":
		sortKey = func(d Device) string { return d.StatsSchema }
	}
	desc := q.Desc
	if q.Sort == "" {
		desc = true // CreatedAt descending
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if desc {
			return sortKey(filtered[i]) > sortKey(filtered[j])
		}
		return sortKey(filtered[i]) < sortKey(filtered[j])
	})

	total := len(filtered)
	lo := q.Offset()
	if lo > total {
		lo = total
	}
	hi := lo + q.Limit()
	if hi > total {
		hi = total
	}

	rows := make([]webui.Row, 0, hi-lo)
	for _, d := range filtered[lo:hi] {
		name := webui.Text(d.Name)
		if d.Revoked {
			name += " " + webui.Pill("revoked", "muted")
		}
		rows = append(rows, webui.Row{
			webui.RowSelectKey: template.HTML(d.ID),
			"reader":           name,
			"lastsync":         webui.Text(orDash(d.LastSyncAt)),
			"koreader":         webui.Text(orDash(d.KOReaderVersion)),
			"token":            codeCell(tokenTail(d.TokenTail)),
			"stats":            schemaCell(d.StatsSchema),
			"actions":          template.HTML(revokeForm(d)),
		})
	}
	return rows, total
}

// servePairDelete is the bulk-delete endpoint the selection toolbar posts to.
func (a *app) servePairDelete(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the selection", http.StatusBadRequest)
		return
	}
	tk := tenant(caller)
	for _, id := range r.Form["id"] {
		a.deleteDevice(r.Context(), tk, strings.TrimSpace(id))
	}
	// The toolbar removes the rows itself on a 2xx; a plain OK is the whole contract.
	w.WriteHeader(http.StatusOK)
}

// deleteDevice removes a reader for good — the record AND its token reference.
//
// REVOKING WAS THE ONLY THING ON OFFER and it is not the same thing. A revoked device
// stops syncing but stays on the page for ever, so a list of readers only ever grows:
// every test pairing, every reader you sold, every code you claimed twice. That is what
// "impossible to delete old devices" meant.
//
// Both records go, and the TOKEN REFERENCE FIRST, matching revokeDevice's order and for
// the same reason: the reference is what an ingest actually loads, so if the second write
// fails the surviving state is a device row nothing can authenticate against, rather than
// a live token pointing at a device that no longer exists.
//
// Deleting is not a security downgrade on revoking: the token was only ever stored as a
// SHA-256, and removing the reference is exactly what makes the bearer token unusable.
func (a *app) deleteDevice(ctx context.Context, t dblib.TenantKey, id string) {
	if id == "" {
		return
	}
	dev, err := a.devices.Get(ctx, t, id)
	if err != nil || dev == nil {
		return
	}
	if dev.TokenSHA256 != "" {
		_ = a.tokens.Delete(ctx, pairCodeTenant(), dev.TokenSHA256)
	}
	_ = a.devices.Delete(ctx, t, dev.ID)
}

// schemaCell renders the statistics schema so it TRUNCATES instead of wrapping.
//
// codeCell's <code> carries overflow-wrap:anywhere, which is right for a token tail and
// wrong for this: `page_stat_data(id_book,page,start_time,duration,total_pages)` is 60-odd
// unbroken characters, so "wrap anywhere" shattered it into a column of fragments and
// dragged every other row's height with it — the "breaks into lines" report.
//
// One line, ellipsised, with the full string on hover and in the ⓘ details modal. The
// value is never hidden, it just stops setting the table's shape.
func schemaCell(s string) template.HTML {
	s = strings.TrimSpace(s)
	if s == "" {
		return webui.Text("—")
	}
	return `<code class="ko-schema" title="` + webui.Text(s) + `">` + webui.Text(s) + `</code>`
}

// koSchemaCSS is the one rule schemaCell needs. Scoped to its own class so it cannot
// reach the token cell, which genuinely does want to wrap.
const koSchemaCSS = `<style>
.ko-schema{display:inline-block;max-width:28ch;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:bottom}
.lv-detail .ko-schema,.lv-modal .ko-schema{max-width:none;white-space:normal;overflow-wrap:anywhere}
</style>`

func revokeForm(d Device) string {
	if d.Revoked {
		return ""
	}
	return fmt.Sprintf(`<form method="post" onsubmit="return confirm('Revoke %s? It will stop syncing immediately.')">`+
		`<input type="hidden" name="action" value="revoke"><input type="hidden" name="device" value="%s">%s</form>`,
		html.EscapeString(d.Name), html.EscapeString(d.ID),
		webui.IconTextButton("fa-solid fa-ban", "Revoke", webui.BtnOpts{Kind: "danger", Size: "sm", Type: "submit"}))
}

// revokeDevice stops a reader syncing.
//
// BOTH RECORDS, and the reference first. deviceForToken checks EITHER flag, so either
// write alone already stops the device — this is defence in depth rather than one
// load-bearing write and one decoration, and both paths are pinned by tests.
//
// The order still matters if the second write fails: leaving a live reference pointing at
// a device flagged dead is the safer half to be stuck in, because the device check is the
// one that runs against the record the ingest actually loads.
func (a *app) revokeDevice(r *http.Request, t dblib.TenantKey, id string) {
	if id == "" {
		return
	}
	dev, err := a.devices.Get(r.Context(), t, id)
	if err != nil || dev == nil {
		return
	}
	if ref, rerr := a.tokens.Get(r.Context(), pairCodeTenant(), dev.TokenSHA256); rerr == nil && ref != nil {
		ref.Revoked = false
		_ = a.tokens.Put(r.Context(), pairCodeTenant(), ref.ID, ref)
	}
	dev.Revoked = true
	_ = a.devices.Put(r.Context(), t, dev.ID, dev)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// codeCell renders a machine value — a token tail, a statistics schema — as a <code>
// chip, and an em dash when there is nothing to show.
//
// It replaces a `class="mono"` on the <td>, which styled NOTHING: the only .mono rule
// in the fleet is `.card-meta .cm-val.mono`, so outside a card-meta list the class
// matched no selector and the values rendered in the body font. <code> is a ROLE the
// shared theme already dresses (STYLEGUIDE §2) — and it carries overflow-wrap:anywhere,
// which is what lets a long unbroken schema string wrap instead of setting the width of
// the whole table.
// tokenTail is the displayed form of a stored token tail — the leading ellipsis is
// what says "the rest of this exists and is not shown". An absent tail stays absent
// rather than becoming a lone ellipsis with nothing behind it.
func tokenTail(tail string) string {
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return "…" + tail
}

func codeCell(s string) template.HTML {
	if strings.TrimSpace(s) == "" {
		return webui.Text("—")
	}
	return "<code>" + webui.Text(s) + "</code>"
}

// --- the device endpoint ----------------------------------------------------

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

type pairReply struct {
	OK         bool   `json:"ok"`
	Token      string `json:"token,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Message    string `json:"message"`
}

// serveAPIPair exchanges a typed code for a device token.
//
// THE TOKEN IS RETURNED EXACTLY ONCE, here. It is never stored in plaintext and there is
// no endpoint that can show it again — a reader that loses it pairs again, which costs
// one code and no secrets.
func (a *app) serveAPIPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, pairReply{Message: "POST a pairing code"})
		return
	}
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, pairReply{Message: "could not read the request"})
		return
	}
	dev, token, err := a.claimPairCode(r.Context(), req.Code, req.DeviceName)
	if err != nil {
		// The reason travels: on an e-ink screen "failed" is indistinguishable from a
		// typo, an expired code and a code somebody else already used.
		writeJSON(w, http.StatusUnauthorized, pairReply{Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pairReply{OK: true, Token: token, DeviceName: dev.Name,
		Message: "Paired as " + dev.Name + "."})
}
