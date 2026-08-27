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
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"xf/controller/pkg/dblib"
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
		default:
			pc, err := newPairCode(tk)
			if err == nil && a.pairCodes.Put(r.Context(), pairCodeTenant(), pc.ID, &pc) == nil {
				minted = pc.ID
			}
		}
	}

	p := a.page("/pair")
	p.StartFor(w, r, "Devices")
	fmt.Fprint(w, webui.PageHeader("Devices", "Readers that send their reading here"))

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
	sort.Slice(devices, func(i, j int) bool { return devices[i].CreatedAt > devices[j].CreatedAt })

	fmt.Fprint(w, `<div class="card"><h3>Paired readers</h3>`)
	if len(devices) == 0 {
		fmt.Fprint(w, `<p class="muted">None yet. Mint a code below, then type it into the plugin on your reader.</p>`)
	} else {
		// webui.SimpleTable, NOT hand-written table markup. This is the row the overflow
		// report pointed at: six columns, one of them a statistics schema like
		// `page_stat_data(id_book,page,start_time,duration,total_pages)`, inside a
		// card. A bare table element gets an overflow container ONLY inside BaseCSS's
		// @media(max-width:640px) — above that it takes its natural width and grows
		// straight out of the card, which is what the screenshot showed at 1916px.
		// SimpleTable renders .lv-tablewrap > .lv-table.tbl-stack, so the wrap scrolls
		// and listViewJS collapses the row to label:value cards at ANY width once it
		// would overflow. Both assets arrive via ListViewHead() in a.page.
		rows := make([]webui.TableRow, 0, len(devices))
		for _, d := range devices {
			name := webui.Text(d.Name)
			if d.Revoked {
				name += " " + webui.Pill("revoked", "muted")
			}
			rows = append(rows, webui.TableRow{Cells: []template.HTML{
				name,
				codeCell(tokenTail(d.TokenTail)),
				webui.Text(orDash(d.LastSyncAt)),
				webui.Text(orDash(d.KOReaderVersion)),
				codeCell(d.StatsSchema),
				template.HTML(revokeForm(d)),
			}})
		}
		// The last header is deliberately EMPTY: the actions column names nothing, and
		// an empty data-label is also what tells the stacked card not to print a label
		// in front of the revoke button.
		fmt.Fprint(w, webui.SimpleTable(
			[]string{"Reader", "Token", "Last sync", "KOReader", "Statistics", ""}, rows))
		fmt.Fprint(w, `<p class="muted">The token is shown only by its last four characters — the rest is stored as a hash and cannot be read back, here or anywhere else.</p>`)
	}
	// One button, so no row container and no margin of its own: the paragraph above it
	// already carries the house paragraph margin. The hand-tuned top margin that used to
	// sit on this form was a literal off the shared scale, which is the drift the style
	// guide's §3 exists to stop — and dynlint's off_scale_spacing counts.
	fmt.Fprintf(w, `<form method="post">%s</form>`,
		webui.IconTextButton("fa-solid fa-key", "Mint a pairing code",
			webui.BtnOpts{Kind: "primary", Type: "submit"}))
	fmt.Fprint(w, `</div>`)
	p.End(w)
}

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
