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
		fmt.Fprint(w, `<div class="card"><h3 style="margin-top:0">Type this on your reader</h3>`)
		// SPACED IN THE MIDDLE and in a monospace face. It is read off one screen and
		// typed on another, at low contrast, by somebody holding both.
		fmt.Fprintf(w, `<p style="font-family:var(--font-mono,ui-monospace,monospace);font-size:2.2rem;letter-spacing:.35rem;margin:.4rem 0">%s</p>`,
			html.EscapeString(minted[:3]+" "+minted[3:]))
		fmt.Fprint(w, `<p class="muted">Valid for fifteen minutes, and it can be used once. On the reader: <strong>☰ → Tools → Zeitgeist reading sync → Pair this device…</strong></p>`)
		fmt.Fprintf(w, `<p class="muted">No plugin yet? <a href="/addon">Install it first</a>.</p></div>`)
	}

	devices, _ := a.devices.List(r.Context(), tk)
	sort.Slice(devices, func(i, j int) bool { return devices[i].CreatedAt > devices[j].CreatedAt })

	fmt.Fprint(w, `<div class="card"><h3 style="margin-top:0">Paired readers</h3>`)
	if len(devices) == 0 {
		fmt.Fprint(w, `<p class="muted">None yet. Mint a code below, then type it into the plugin on your reader.</p>`)
	} else {
		fmt.Fprint(w, `<table><thead><tr><th>Reader</th><th>Token</th><th>Last sync</th><th>KOReader</th><th>Statistics</th><th></th></tr></thead><tbody>`)
		for _, d := range devices {
			state := ""
			if d.Revoked {
				state = " " + string(webui.Pill("revoked", "muted"))
			}
			fmt.Fprintf(w, `<tr><td>%s%s</td><td class="mono">…%s</td><td>%s</td><td>%s</td><td class="mono">%s</td><td>%s</td></tr>`,
				html.EscapeString(d.Name), state,
				html.EscapeString(d.TokenTail),
				html.EscapeString(orDash(d.LastSyncAt)),
				html.EscapeString(orDash(d.KOReaderVersion)),
				html.EscapeString(orDash(d.StatsSchema)),
				revokeForm(d))
		}
		fmt.Fprint(w, `</tbody></table>`)
		fmt.Fprint(w, `<p class="muted" style="margin-top:.6rem">The token is shown only by its last four characters — the rest is stored as a hash and cannot be read back, here or anywhere else.</p>`)
	}
	fmt.Fprintf(w, `<form method="post" style="margin-top:.8rem">%s</form>`,
		webui.IconTextButton("fa-solid fa-key", "Mint a pairing code", webui.BtnOpts{Kind: "primary", Type: "submit"}))
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
