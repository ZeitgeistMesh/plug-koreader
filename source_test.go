package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xf/controller/pkg/secretbox"
)

// calibreWeb is a stand-in for a Calibre-Web OPDS server: Basic auth on everything, a root of
// navigation entries, an alphabetical index split by letter, and one paged "recent" listing.
func calibreWeb(t *testing.T, user, pass string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	nav := func(title, href string) string {
		return fmt.Sprintf(`<entry><title>%s</title><id>n:%s</id><link rel="subsection" href="%s" type="application/atom+xml;profile=opds-catalog;kind=navigation"/></entry>`, title, href, href)
	}
	book := func(id, title, author string) string {
		return fmt.Sprintf(`<entry><title>%s</title><id>urn:uuid:%s</id><updated>2026-01-01T00:00:00Z</updated><author><name>%s</name></author>`+
			`<summary>&lt;p&gt;About %s&lt;/p&gt;</summary>`+
			`<link rel="http://opds-spec.org/acquisition" href="/opds/download/%s/epub/" type="application/epub+zip"/>`+
			`<link rel="http://opds-spec.org/image" href="/opds/cover/%s" type="image/jpeg"/></entry>`, title, id, author, title, id, id)
	}
	wrap := func(inner, extra string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Library</title>` + extra + inner + `</feed>`
	}
	mux.HandleFunc("/opds", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, wrap(nav("Alphabetical Books", "/opds/books")+nav("Authors", "/opds/author"), ""))
	})
	mux.HandleFunc("/opds/books", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, wrap(nav("A", "/opds/books/a")+nav("B", "/opds/books/b"), ""))
	})
	mux.HandleFunc("/opds/books/a", func(w http.ResponseWriter, r *http.Request) {
		next := ""
		if r.URL.Query().Get("page") == "" {
			next = `<link rel="next" href="/opds/books/a?page=2" type="application/atom+xml"/>`
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, wrap(book("3", "Anna Karenina", "Leo Tolstoy"), ""))
			return
		}
		fmt.Fprint(w, wrap(book("1", "Alice in Wonderland", "Lewis Carroll")+book("2", "Antifragile", "Nassim Taleb"), next))
	})
	mux.HandleFunc("/opds/books/b", func(w http.ResponseWriter, r *http.Request) {
		// a link that names another origin must be dropped, not followed
		other := `<entry><title>Elsewhere</title><id>x</id><link rel="http://opds-spec.org/acquisition" href="https://evil.example/book.epub" type="application/epub+zip"/></entry>`
		fmt.Fprint(w, wrap(book("4", "Brave New World", "Aldous Huxley")+other, ""))
	})
	mux.HandleFunc("/opds/author", func(w http.ResponseWriter, r *http.Request) { t.Error("an author index must not be crawled") })
	mux.HandleFunc("/opds/download/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("EPUBDATA")) })
	mux.HandleFunc("/opds/cover/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64)))
	})
	mux.HandleFunc("/bounce", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/opds", http.StatusFound)
	})
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if user != "" && (!ok || u != user || p != pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="calibre"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml;profile=opds-catalog")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// clientTo is a client for the test server: it trusts the server's certificate and skips the
// outbound guard, which refuses loopback by design (and is tested on its own below).
func clientTo(t *testing.T, srv *httptest.Server, user, pass string) *opdsClient {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	hc := srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &opdsClient{http: hc, origin: u, user: user, password: pass}
}

func TestCrawlReadsTheCatalogThroughLettersAndPages(t *testing.T) {
	srv := calibreWeb(t, "reader", "s3cret")
	c := clientTo(t, srv, "reader", "s3cret")
	root, f, err := c.findRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(root, "/opds") {
		t.Errorf("root = %s", root)
	}
	entries, cut, err := c.crawl(context.Background(), root, f)
	if err != nil || cut {
		t.Fatalf("crawl: %v cut=%v", err, cut)
	}
	var titles []string
	for _, e := range entries {
		titles = append(titles, e.Title)
	}
	if got := strings.Join(titles, "|"); got != "Alice in Wonderland|Antifragile|Anna Karenina|Brave New World" {
		t.Errorf("titles = %s", got)
	}
	e := entries[0]
	if e.Authors != "Lewis Carroll" || e.Summary != "About Alice in Wonderland" || len(e.Files) != 1 || e.Files[0].Type != "application/epub+zip" ||
		!strings.HasPrefix(e.Files[0].Href, srv.URL+"/opds/download/") || !strings.HasPrefix(e.Cover, srv.URL+"/opds/cover/") {
		t.Errorf("entry = %+v", e)
	}
	for _, e := range entries {
		if e.Title == "Elsewhere" {
			t.Error("a book whose only link names another origin must be dropped")
		}
	}
}

func TestAWrongPasswordIsRefusedAndTheRightOneIsNot(t *testing.T) {
	srv := calibreWeb(t, "reader", "s3cret")
	if _, _, err := clientTo(t, srv, "reader", "wrong").findRoot(context.Background()); err == nil || err.Error() != ui.ErrAuth {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := clientTo(t, srv, "", "").findRoot(context.Background()); err == nil || err.Error() != ui.ErrAuth {
		t.Errorf("no credentials: %v", err)
	}
	if _, _, err := clientTo(t, srv, "reader", "s3cret").findRoot(context.Background()); err != nil {
		t.Errorf("right password: %v", err)
	}
}

func TestARedirectToAnotherOriginIsNotFollowed(t *testing.T) {
	srv := calibreWeb(t, "", "")
	_, err := clientTo(t, srv, "", "").fetchFeed(context.Background(), srv.URL+"/bounce")
	if err == nil || err.Error() != ui.ErrRedirectOff {
		t.Errorf("err = %v", err)
	}
}

func TestOPDS2IsReadToo(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/opds+json")
		page := map[string]any{
			"metadata": map[string]any{"title": "Komga"},
			"links":    []any{map[string]any{"rel": "next", "href": "/opds2?page=2"}},
			"publications": []any{map[string]any{
				"metadata": map[string]any{"identifier": "urn:b1", "title": "Dune", "author": []any{map[string]any{"name": "Frank Herbert"}}, "description": "<b>Sand</b>"},
				"links":    []any{map[string]any{"rel": "http://opds-spec.org/acquisition/open-access", "href": "/f/1", "type": "application/epub+zip"}},
				"images":   []any{map[string]any{"href": "/c/1", "type": "image/jpeg"}},
			}},
		}
		if r.URL.Query().Get("page") == "2" {
			page["links"] = []any{}
			page["publications"] = []any{map[string]any{
				"metadata": map[string]any{"identifier": "urn:b2", "title": map[string]any{"en": "Emma"}, "author": "Jane Austen"},
				"links":    []any{map[string]any{"rel": []any{"http://opds-spec.org/acquisition"}, "href": "/f/2", "type": "application/pdf"}},
			}}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	c := clientTo(t, srv, "", "")
	root, f, err := c.findRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	es, _, err := c.crawl(context.Background(), root, f)
	if err != nil || len(es) != 2 || es[0].Title != "Dune" || es[0].Authors != "Frank Herbert" || es[0].Summary != "Sand" ||
		es[1].Title != "Emma" || es[1].Authors != "Jane Austen" || es[1].Files[0].Type != "application/pdf" {
		t.Fatalf("entries = %+v, %v", es, err)
	}
	_ = srv
}

// The guard, not the form, is what stops a connection to a private address: a public name that
// resolves to loopback is refused when the socket is dialled.
func TestTheGuardRefusesLoopbackAtDialTime(t *testing.T) {
	srv := calibreWeb(t, "", "")
	c, err := newOPDSClient(srv.URL, "", "", false) // https://127.0.0.1:port
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.findRoot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot be used here") {
		t.Errorf("err = %v", err)
	}
}

func TestSourceAddressesAreChecked(t *testing.T) {
	for in, ok := range map[string]bool{
		"https://books.zm.is":          true,
		"books.zm.is":                  true,
		"https://books.zm.is/calibre/": true,
		"http://books.zm.is":           false,
		"https://127.0.0.1":            false,
		"https://localhost":            false,
		"https://10.0.0.5/opds":        false,
		"https://192.168.1.2":          false,
		"https://169.254.169.254":      false,
		"https://100.64.1.1":           false,
		"https://user:pw@books.zm.is":  false,
		"":                             false,
		"ftp://books.zm.is":            false,
		"https://[::1]/opds":           false,
	} {
		got, err := cleanSourceURL(in, false)
		if (err == nil) != ok {
			t.Errorf("%q: err=%v want ok=%v", in, err, ok)
		}
		if ok && (strings.Contains(got, "@") || !strings.HasPrefix(got, "https://") || strings.HasSuffix(got, "/")) {
			t.Errorf("%q cleaned to %q", in, got)
		}
	}
}

func TestSavingEncryptsThePasswordAndNothingReturnsIt(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	me := "m@zm.is"
	tk := tenant(me)
	res := a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://books.zm.is", "username": "mark", "password": "hunter2-secret"})
	if res["error"] != nil || res["password"] != "set" {
		t.Fatalf("set: %v", res)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "hunter2") {
		t.Error("the answer carries the password")
	}
	s := a.loadSource(ctx, tk)
	if s.Secret == "" || strings.Contains(s.Secret, "hunter2") || !strings.HasPrefix(s.Secret, "enc:") {
		t.Errorf("stored secret = %q", s.Secret)
	}
	if a.secrets.Open(s.Secret) != "hunter2-secret" {
		t.Error("the sealed password does not open")
	}
	get, _ := json.Marshal(a.handleSourceGet(ctx, me, tk, map[string]any{}))
	if strings.Contains(string(get), "hunter2") || !strings.Contains(string(get), `"password":"set"`) {
		t.Errorf("get = %s", get)
	}
	// A blank password keeps the saved one for the same address ...
	a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://books.zm.is", "username": "mark2"})
	if a.secrets.Open(a.loadSource(ctx, tk).Secret) != "hunter2-secret" {
		t.Error("a blank password should keep the saved one")
	}
	// ... but is never carried to another server.
	a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://other.example.com", "username": "mark"})
	if a.loadSource(ctx, tk).Secret != "" {
		t.Error("the password followed the user to a different server")
	}
}

func TestNoKeyMeansNoPasswordIsStored(t *testing.T) {
	a := testApp()
	a.secrets = secretbox.Guard{Box: secretbox.New(""), Name: "TEST_"}
	res := a.handleSourceSet(context.Background(), "m@zm.is", tenant("m@zm.is"), map[string]any{"url": "https://books.zm.is", "username": "mark", "password": "hunter2"})
	if res["error"] != ui.ErrNoKey {
		t.Errorf("res = %v", res)
	}
	if a.loadSource(context.Background(), tenant("m@zm.is")) != nil {
		t.Error("something was saved without a key")
	}
}

func TestAnAgentCannotManageTheServerOrSeeItsDetails(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	me := "m@zm.is"
	tk := tenant(me)
	a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://books.zm.is", "username": "mark", "password": "pw"})
	agent := map[string]any{"_from": "agent.zm.is"}
	for name, res := range map[string]map[string]any{
		"set":     a.handleSourceSet(ctx, me, tk, merge(agent, map[string]any{"url": "https://x.example.com"})),
		"test":    a.handleSourceTest(ctx, me, tk, agent),
		"delete":  a.handleSourceDelete(ctx, me, tk, agent),
		"refresh": a.handleCatalogRefresh(ctx, me, tk, agent),
	} {
		if res["error"] != ui.ErrAgentOnlyYou {
			t.Errorf("%s by an agent = %v", name, res)
		}
	}
	got, _ := json.Marshal(a.handleSourceGet(ctx, me, tk, agent))
	if strings.Contains(string(got), "books.zm.is") || strings.Contains(string(got), "mark") {
		t.Errorf("an agent saw the details: %s", got)
	}
	if a.loadSource(ctx, tk) == nil {
		t.Error("the agent's delete went through")
	}
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestCatalogSearchAndDue(t *testing.T) {
	es := []CatalogEntry{{ID: "1", Title: "Alice in Wonderland", Authors: "Lewis Carroll"}, {ID: "2", Title: "Antifragile", Authors: "Nassim Taleb"}}
	if got := searchCatalog(es, "carroll alice"); len(got) != 1 || got[0].ID != "1" {
		t.Errorf("search = %v", got)
	}
	if got := searchCatalog(es, ""); len(got) != 2 {
		t.Errorf("empty search = %v", got)
	}
	now := time.Now()
	s := &Source{URL: "https://a.example.com"}
	fresh := &Catalog{SourceURL: s.URL, RefreshedAt: now.Add(-time.Hour).Format(time.RFC3339)}
	old := &Catalog{SourceURL: s.URL, RefreshedAt: now.Add(-7 * time.Hour).Format(time.RFC3339)}
	failed := &Catalog{SourceURL: s.URL, Error: "x", TriedAt: now.Add(-time.Minute).Format(time.RFC3339), RefreshedAt: now.Add(-time.Hour).Format(time.RFC3339)}
	other := &Catalog{SourceURL: "https://b.example.com", RefreshedAt: now.Format(time.RFC3339)}
	for name, tc := range map[string]struct {
		c    *Catalog
		s    *Source
		want bool
	}{"none": {nil, s, true}, "fresh": {fresh, s, false}, "old": {old, s, true}, "failed lately": {failed, s, false}, "other server": {other, s, true}, "no source": {nil, nil, false}} {
		if got := catalogDue(tc.c, tc.s, now); got != tc.want {
			t.Errorf("%s: due=%v want %v", name, got, tc.want)
		}
	}
}

func TestFileCacheExpiresAndStaysUnderItsCap(t *testing.T) {
	dir := t.TempDir()
	fc := newFileCache(dir, 100, time.Hour, 60)
	tk := tenant("m@zm.is")
	put := func(name string, n int, age time.Duration) string {
		p := fc.path(tk, name)
		if err := fc.store(p, strings.NewReader(strings.Repeat("x", n))); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, time.Now().Add(-age), time.Now().Add(-age))
		return p
	}
	if err := fc.store(fc.path(tk, "big"), strings.NewReader(strings.Repeat("x", 61))); err == nil {
		t.Error("a file over the per-file limit was kept")
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*", ".part-*")); len(entries) != 0 {
		t.Errorf("a partial file was left behind: %v", entries)
	}
	oldest := put("a", 50, 3*time.Minute)
	mid := put("b", 40, 2*time.Minute)
	put("c", 40, time.Minute) // total 130 > cap 100: the oldest goes
	fc.evict()
	if _, err := os.Stat(oldest); err == nil {
		t.Error("the oldest file should have been evicted")
	}
	if _, err := os.Stat(mid); err != nil {
		t.Error("the newer files should stay")
	}
	expired := put("d", 5, 2*time.Hour)
	fc.evict()
	if _, err := os.Stat(expired); err == nil {
		t.Error("an expired file was kept")
	}
	if fc.fresh(expired) {
		t.Error("an expired file counts as fresh")
	}
	fc.dropTenant(tk)
	if _, err := os.Stat(filepath.Join(dir, tenantDir(tk))); err == nil {
		t.Error("dropTenant left the directory")
	}
}

func TestAddressCheckRefusesLoopbackSoThePageNeverSavesIt(t *testing.T) {
	a := testApp()
	res := a.handleSourceSet(context.Background(), "m@zm.is", tenant("m@zm.is"), map[string]any{"url": "https://127.0.0.1:8443"})
	if res["error"] == nil {
		t.Errorf("loopback was saved: %v", res)
	}
}

func TestSourceCapabilitiesAreOffMCPExceptTheCatalogList(t *testing.T) {
	for _, c := range append(sourceCapabilities(), catalogCapabilities()...) {
		want := c.Name == capCatalogList
		if c.ExposeToMCP != want {
			t.Errorf("%s ExposeToMCP = %v", c.Name, c.ExposeToMCP)
		}
		if c.Command != c.Name || len(c.RequiredScopes) == 0 {
			t.Errorf("%s: command/scopes missing", c.Name)
		}
	}
}

// With the book server reached through a test client, the whole path runs: save, test, read the
// catalog into the saved copy, then fetch a book and a cover on demand into the cache.
func TestRefreshThenFetchOnDemand(t *testing.T) {
	srv := calibreWeb(t, "reader", "s3cret")
	a := testApp()
	a.clientFn = func(s *Source) (*opdsClient, error) {
		return clientTo(t, srv, s.Username, a.secrets.Open(s.Secret)), nil
	}
	ctx := context.Background()
	me := "m@zm.is"
	tk := tenant(me)
	if res := a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://books.zm.is", "username": "reader", "password": "s3cret"}); res["error"] != nil {
		t.Fatal(res)
	}
	if res := a.handleSourceTest(ctx, me, tk, map[string]any{}); res["ok"] != true {
		t.Fatalf("test = %v", res)
	}
	if s := a.loadSource(ctx, tk); s.TestedAt == "" || s.TestError != "" {
		t.Errorf("the test result was not recorded: %+v", s)
	}
	a.refreshCatalog(ctx, tk)
	c := a.loadCatalog(ctx, tk)
	if c == nil || len(c.Entries) != 4 || c.Error != "" || c.Entries[0].Title != "Alice in Wonderland" {
		t.Fatalf("catalog = %+v", c)
	}
	list := a.handleCatalogList(ctx, me, tk, map[string]any{"query": "tolstoy"})
	if list["count"] != 1 {
		t.Errorf("list = %v", list)
	}
	e := findEntry(c, entryKey(c.Entries[0].ID))
	s := a.loadSource(ctx, tk)
	p, err := a.fetchToCache(ctx, tk, s, entryKey(e.ID)+"-0.epub", e.Files[0].Href)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "EPUBDATA" {
		t.Errorf("book = %q", b)
	}
	srv.Close() // a second fetch is served from the cache, not the server
	if p2, err := a.fetchToCache(ctx, tk, s, entryKey(e.ID)+"-0.epub", e.Files[0].Href); err != nil || p2 != p {
		t.Errorf("second fetch = %s, %v", p2, err)
	}
	// A failed refresh keeps the saved list and says why.
	a.refreshCatalog(ctx, tk)
	c = a.loadCatalog(ctx, tk)
	if len(c.Entries) != 4 || c.Error == "" {
		t.Errorf("after a failed refresh: %d entries, error %q", len(c.Entries), c.Error)
	}
	if res := a.handleSourceDelete(ctx, me, tk, map[string]any{}); res["error"] != nil || a.loadCatalog(ctx, tk) != nil || a.loadSource(ctx, tk) != nil {
		t.Errorf("delete left something: %v", res)
	}
}

func TestSettingsPageNeverCarriesThePassword(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	me := "m@zm.is"
	tk := tenant(me)
	a.handleSourceSet(ctx, me, tk, map[string]any{"url": "https://books.zm.is", "username": "mark", "password": "hunter2-secret"})
	r := httptest.NewRequest(http.MethodGet, "/settings", nil)
	body := a.settingsBody(r, tk, "", false)
	if strings.Contains(body, "hunter2") || strings.Contains(body, "enc:") {
		t.Error("the settings page carries the password or its sealed form")
	}
	for _, want := range []string{`value="https://books.zm.is"`, `value="mark"`, `type="password"`, ui.PlaceholderPasswordSet, `hx-post="/settings"`, ui.BtnDisconnect} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(a.settingsBody(r, tenant("other@zm.is"), "", false), "books.zm.is") {
		t.Error("another person's page shows this server")
	}
}

type fakeRoles map[string]bool

func (f fakeRoles) Holds(_ context.Context, role, subject, _ string) bool {
	return f[role+"|"+subject]
}

// The tailnet is reachable only for a person who holds the iam role, asked live.
func TestTailnetNeedsTheRole(t *testing.T) {
	a := testApp()
	a.iam = fakeRoles{"iam.tailnet.use|owner@zm.is": true}
	ctx := context.Background()
	if !a.tailnetOK(ctx, "owner@zm.is") || a.tailnetOK(ctx, "customer@zm.is") || a.tailnetOK(ctx, "") {
		t.Error("tailnetOK does not follow the role")
	}
	if _, err := cleanSourceURL("https://100.64.1.1", false); err == nil {
		t.Error("a tailnet address was accepted without the role")
	}
	if _, err := cleanSourceURL("https://100.64.1.1", true); err != nil {
		t.Errorf("a tailnet address was refused to the role holder: %v", err)
	}
	if _, err := cleanSourceURL("https://10.0.0.5", true); err == nil {
		t.Error("a private address was accepted even with the role")
	}
	a.iam = nil
	if a.tailnetOK(ctx, "owner@zm.is") {
		t.Error("no iam client must mean no tailnet")
	}
}

