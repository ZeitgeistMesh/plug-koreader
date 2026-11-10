package main

// source.go — the book server a person connects, and the four capabilities that manage it.
//
// One server per person, kept under a fixed id in their own tenant. The password is sealed
// with secretbox before it is stored; with no key the write is refused (never plaintext), and
// no capability ever returns it: get says only that one is set. The address is checked when it
// is saved for a friendly message, and again on every connection the guard opens, which is the
// check that holds.

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"xf/controller/pkg/agentprivacy"
	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
	"xf/controller/pkg/outbound"
	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/schemakit"
	"xf/controller/pkg/secretbox"
	"xf/controller/pkg/xats"
)

const (
	collSource  = "reader_source"
	sourceDocID = "source"

	capSourceSet    = "reader.source.set"
	capSourceGet    = "reader.source.get"
	capSourceTest   = "reader.source.test"
	capSourceDelete = "reader.source.delete"
)

// Source is the person's book server.
type Source struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	// Secret is the password, sealed. Never sent to a page, a capability answer or a log.
	Secret    string `json:"secret,omitempty"`
	Root      string `json:"root,omitempty"` // the feed address found at the last test
	TestedAt  string `json:"tested_at,omitempty"`
	TestError string `json:"test_error,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// cleanSourceURL checks an address a person typed and returns it without credentials, query or
// fragment. Everything except a public https address is refused: the guard would refuse the
// connection anyway, this says why at the form.
func cleanSourceURL(raw string, tailnetOK bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New(ui.ErrNoURL)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", errors.New(ui.ErrNotWeb)
	}
	if u.Scheme != "https" {
		return "", errors.New(ui.ErrHTTPS)
	}
	if _, err := outbound.ValidateURL(raw, tailnetOK); err != nil {
		return "", err
	}
	return "https://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

func (a *app) loadSource(ctx context.Context, tk dblib.TenantKey) *Source {
	s, err := a.sources.Get(ctx, tk, sourceDocID)
	if err != nil || s == nil || s.URL == "" {
		return nil
	}
	return s
}

// clientFor builds the OPDS client for a saved source.
func (a *app) clientFor(ctx context.Context, subject string, s *Source) (*opdsClient, error) {
	if a.clientFn != nil { // a test reaches a server on loopback, which the guard refuses
		return a.clientFn(s)
	}
	return newOPDSClient(s.URL, s.Username, a.secrets.Open(s.Secret), a.tailnetOK(ctx, subject))
}

// roleHolder is the part of *iam.Client the plug needs, behind an interface so a test can say
// who holds the role without a mesh.
type roleHolder interface {
	Holds(ctx context.Context, role, subject, resource string) bool
}

// tailnetOK is asked live on every connection, so revoking the role takes effect at once. With
// no iam client (or none that answers) it is false: the guard then refuses tailnet addresses.
func (a *app) tailnetOK(ctx context.Context, subject string) bool {
	return a.iam != nil && subject != "" && a.iam.Holds(ctx, outbound.TailnetRole, subject, "")
}

// sourceView is the safe description of a source: what a page or a caller may be told.
func sourceView(s *Source) map[string]any {
	if s == nil {
		return map[string]any{"configured": false, "body": ui.StatusNone}
	}
	out := map[string]any{
		"configured": true, "url": s.URL, "username": s.Username, "password": "",
		"tested_at": s.TestedAt, "test_error": s.TestError, "body": "Connected.",
	}
	if s.Secret != "" {
		out["password"] = "set"
	}
	return out
}

func errMap(msg string) map[string]any { return map[string]any{"error": msg, "body": msg} }

func (a *app) handleSourceGet(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	s := a.loadSource(ctx, tk)
	if agentprivacy.FromAgent(meta) {
		// An agent learns whether one is connected, not where or as whom.
		return map[string]any{"configured": s != nil, "body": "Connected."}
	}
	return sourceView(s)
}

// handleSourceSet saves the address, the username and, when given, the password. A blank
// password keeps the saved one, but only while the address is the same: a password is never
// carried to a different server.
func (a *app) handleSourceSet(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	if _, demoVisitor := demo.NonceFromSubject(caller); demoVisitor {
		return errMap(ui.ErrDemo)
	}
	base, err := cleanSourceURL(plugrun.Str(meta, "url"), a.tailnetOK(ctx, caller))
	if err != nil {
		return errMap(err.Error())
	}
	user := strings.TrimSpace(plugrun.Str(meta, "username"))
	pass := plugrun.Str(meta, "password")
	if len(user) > 200 || len(pass) > 400 {
		return errMap(ui.ErrTooLong)
	}
	old := a.loadSource(ctx, tk)
	s := &Source{ID: sourceDocID, URL: base, Username: user, Subject: tk.Subject, Workspace: tk.Workspace, CreatedAt: nowRFC()}
	if old != nil {
		s.CreatedAt = old.CreatedAt
	}
	switch {
	case pass != "":
		sealed, err := a.secrets.Seal(pass)
		if err != nil {
			if errors.Is(err, secretbox.ErrPlaintextRefused) || errors.Is(err, secretbox.ErrNoKey) {
				return errMap(ui.ErrNoKey)
			}
			return errMap(ui.ErrStore)
		}
		s.Secret = sealed
	case old != nil && old.URL == base:
		s.Secret = old.Secret
	}
	s.UpdatedAt = nowRFC()
	if err := a.sources.Put(ctx, tk, sourceDocID, s); err != nil {
		return errMap(ui.ErrSave)
	}
	if old == nil || old.URL != base || old.Username != user || pass != "" {
		a.dropCatalog(ctx, tk)
	}
	return sourceView(s)
}

// handleSourceTest connects and reports what it found. With a url in meta it tests those
// details without saving them (a blank password reuses the saved one for the same address);
// without, it tests what is saved and records the result beside it.
func (a *app) handleSourceTest(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	saved := a.loadSource(ctx, tk)
	s, record := saved, true
	if raw := plugrun.Str(meta, "url"); strings.TrimSpace(raw) != "" {
		base, err := cleanSourceURL(raw, a.tailnetOK(ctx, caller))
		if err != nil {
			return errMap(err.Error())
		}
		s, record = &Source{URL: base, Username: strings.TrimSpace(plugrun.Str(meta, "username"))}, false
		if pass := plugrun.Str(meta, "password"); pass != "" {
			c, err := newOPDSClient(s.URL, s.Username, pass, a.tailnetOK(ctx, caller))
			if err != nil {
				return errMap(err.Error())
			}
			return testResult(ctx, c, nil, tk, a)
		}
		if saved != nil && saved.URL == base {
			s.Secret = saved.Secret
		}
	}
	if s == nil {
		return errMap(ui.ErrConnectFirst)
	}
	c, err := a.clientFor(ctx, caller, s)
	if err != nil {
		return errMap(err.Error())
	}
	if !record {
		return testResult(ctx, c, nil, tk, a)
	}
	return testResult(ctx, c, s, tk, a)
}

// testResult runs the connection test; with rec set it stores the outcome on that source.
func testResult(ctx context.Context, c *opdsClient, rec *Source, tk dblib.TenantKey, a *app) map[string]any {
	root, f, err := c.findRoot(ctx)
	if rec != nil {
		rec.TestedAt = nowRFC()
		rec.TestError = ""
		if err != nil {
			rec.TestError = err.Error()
		} else {
			rec.Root = root
		}
		_ = a.sources.Put(ctx, tk, sourceDocID, rec)
	}
	if err != nil {
		return errMap(err.Error())
	}
	return map[string]any{"ok": true, "title": f.Title, "body": fill(ui.StatusOK, "server", serverName(map[string]any{"title": f.Title}))}
}

func (a *app) handleSourceDelete(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	if err := a.sources.Delete(ctx, tk, sourceDocID); err != nil {
		return errMap(ui.ErrSave)
	}
	a.dropCatalog(ctx, tk)
	a.files.dropTenant(tk)
	return map[string]any{"configured": false, "body": ui.StatusDisconnected}
}

func sourceCapabilities() []xats.Capability {
	sec := []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}}
	mk := func(name, title, desc, icon string, ro bool, in map[string]any) xats.Capability {
		c := xats.Capability{
			Name: name, Command: name, Title: title, Description: desc, Icon: icon,
			RequiredScopes: []string{scopeUser}, Security: sec,
			// Not on MCP: these carry a password or act on one. An agent can ask whether a
			// server is connected and nothing more (and only through get).
			ExposeToMCP: false,
			InputSchema: in,
		}
		if ro {
			c.Annotations = map[string]any{"readOnlyHint": true}
		}
		return c
	}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return []xats.Capability{
		mk(capSourceGet, "Book Server", "Which book server is connected: its address and username. The password is never returned, only whether one is set.",
			"fa-solid fa-server", true, obj(map[string]any{})),
		mk(capSourceSet, "Connect a Book Server", "Save the address, username and password of your book server (Calibre-Web, calibre, Kavita, Komga or any OPDS server). "+
			"A blank password keeps the saved one. The password is stored encrypted.",
			"fa-solid fa-plug", false, obj(map[string]any{
				"url":      schemakit.StrD("the server's https address, like https://books.example.com"),
				"username": schemakit.StrD("the account name on that server; blank when it needs none"),
				"password": schemakit.StrD("the account password; blank keeps the saved one"),
			})),
		mk(capSourceTest, "Test the Book Server", "Connect and say whether the server answers with a catalog. With an address it tests those details without saving them; without, it tests the saved ones.",
			"fa-solid fa-circle-check", true, obj(map[string]any{
				"url":      schemakit.StrD("an address to try instead of the saved one"),
				"username": schemakit.StrD("the account name to try"),
				"password": schemakit.StrD("the password to try; blank reuses the saved one for the same address"),
			})),
		mk(capSourceDelete, "Disconnect the Book Server", "Forget the connected server, its password and the saved list of books.",
			"fa-solid fa-link-slash", false, obj(map[string]any{})),
	}
}

