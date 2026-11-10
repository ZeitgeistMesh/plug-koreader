package main

// opds.go — a client for the book servers people connect: OPDS 1.2 (Atom) and OPDS 2 (JSON).
//
// Calibre-Web, calibre's content server, Kavita and Komga all publish a catalog this way, so
// one client serves them all. The client never decides where it may connect: every socket is
// opened through the outbound guard, which refuses loopback, private, link-local and tailnet
// addresses at dial time, after DNS, on every hop. On top of that it keeps the book server's
// credentials on the book server's own origin: a link in a feed that names another origin is
// dropped, never followed, and a redirect to another origin is an error.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/outbound"
)

const (
	maxFeedBytes  = 8 << 20 // one feed page
	maxRedirects  = 5
	maxCatalogLen = 5000 // books kept per person
	maxFeedPages  = 120  // requests per refresh
)

// BookFile is one way to get a book: its address on the server and its media type.
type BookFile struct {
	Href string `json:"href"`
	Type string `json:"type"`
}

// CatalogEntry is one book as the server lists it.
type CatalogEntry struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Authors string     `json:"authors,omitempty"`
	Summary string     `json:"summary,omitempty"`
	Updated string     `json:"updated,omitempty"`
	Cover   string     `json:"cover,omitempty"`
	Files   []BookFile `json:"files,omitempty"`
}

// errAuth is a refused login, told apart from a network failure.
var errAuth = errors.New(ui.ErrAuth)

// opdsClient talks to one book server as one person.
type opdsClient struct {
	http     *http.Client
	origin   *url.URL
	user     string
	password string
}

// bookGuard judges every connection a person's book-server address opens. Enforcing from the
// first request: this plug is new, so there is no log-only phase to wait out.
var bookGuard = outbound.NewEnforcing("plug-reader")

// newOPDSClient builds a client for one person. tailnetOK says whether they may reach a
// tailnet address (the iam role outbound.TailnetRole, asked live by the caller): a server
// whose name resolves there, as the owner's own does from inside the mesh, is refused to
// everyone else.
func newOPDSClient(base, user, password string, tailnetOK bool) (*opdsClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New(ui.ErrNotWeb)
	}
	return &opdsClient{http: bookGuard.Client(tailnetOK, 25*time.Second), origin: u, user: user, password: password}, nil
}

// sameOrigin reports whether href, resolved against base, stays on the server's own origin.
func (c *opdsClient) sameOrigin(u *url.URL) bool {
	return u.Scheme == c.origin.Scheme && strings.EqualFold(u.Host, c.origin.Host)
}

// resolve turns a link from a feed into an absolute address on the server, or "" when it
// names anything else.
func (c *opdsClient) resolve(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := b.Parse(href)
	if err != nil || !c.sameOrigin(u) {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// get fetches an address on the server's origin with Basic auth, following a redirect only
// while it stays on that origin. The caller closes the body.
func (c *opdsClient) get(ctx context.Context, target, accept string) (*http.Response, error) {
	for hop := 0; hop <= maxRedirects; hop++ {
		u, err := url.Parse(target)
		if err != nil || !c.sameOrigin(u) {
			return nil, errors.New(ui.ErrRedirectOff)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "Zeitgeist-Reader/1.0")
		if c.user != "" || c.password != "" {
			req.SetBasicAuth(c.user, c.password)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, errors.New(fill(ui.ErrUnreachable, "why", unwrapDial(err).Error()))
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.Header.Get("Location") != "":
			next := c.resolve(target, resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if next == "" {
				return nil, errors.New(ui.ErrRedirectOff)
			}
			target = next
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			_ = resp.Body.Close()
			return nil, errAuth
		case resp.StatusCode == http.StatusNotFound:
			_ = resp.Body.Close()
			return nil, errNotFound
		case resp.StatusCode != http.StatusOK:
			_ = resp.Body.Close()
			return nil, errors.New(fill(ui.ErrStatus, "code", fmt.Sprint(resp.StatusCode)))
		}
		return resp, nil
	}
	return nil, errors.New(ui.ErrRedirects)
}

var errNotFound = errors.New(ui.ErrNoCatalogFound)

// unwrapDial keeps the part of a dial error a person can act on.
func unwrapDial(err error) error {
	if errors.Is(err, outbound.ErrDenied) {
		return errors.New(ui.ErrAddressBlocked)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// feed is one parsed page of a catalog, whichever dialect it came in.
type feed struct {
	Title   string
	Next    string
	Nav     []navLink
	Entries []CatalogEntry
	Crawl   string // the server's crawlable feed, when it names one
	Search  string
}

type navLink struct {
	Title string
	Href  string
	Rel   string
}

const acceptFeeds = "application/opds+json, application/atom+xml;profile=opds-catalog, application/atom+xml, application/json;q=0.8, */*;q=0.5"

// fetchFeed reads and parses one feed page.
func (c *opdsClient) fetchFeed(ctx context.Context, target string) (*feed, error) {
	resp, err := c.get(ctx, target, acceptFeeds)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, errors.New(fill(ui.ErrUnreachable, "why", err.Error()))
	}
	if len(body) > maxFeedBytes {
		return nil, errors.New(ui.ErrTooLarge)
	}
	return c.parseFeed(target, resp.Header.Get("Content-Type"), body)
}

func (c *opdsClient) parseFeed(base, contentType string, body []byte) (*feed, error) {
	trim := strings.TrimSpace(string(body[:min(len(body), 64)]))
	if strings.Contains(contentType, "json") || strings.HasPrefix(trim, "{") {
		return c.parseOPDS2(base, body)
	}
	if !strings.HasPrefix(trim, "<") {
		return nil, errors.New(ui.ErrNotCatalog)
	}
	return c.parseAtom(base, body)
}

// ── OPDS 1.2 (Atom) ─────────────────────────────────────────────────────────

type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

type atomEntry struct {
	ID      string     `xml:"id"`
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Authors []string   `xml:"author>name"`
	Summary string     `xml:"summary"`
	Content string     `xml:"content"`
	Links   []atomLink `xml:"link"`
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

func (c *opdsClient) parseAtom(base string, body []byte) (*feed, error) {
	var af atomFeed
	d := xml.NewDecoder(strings.NewReader(string(body)))
	d.Strict = false
	if err := d.Decode(&af); err != nil || af.XMLName.Local != "feed" {
		return nil, errors.New(ui.ErrNotCatalog)
	}
	f := &feed{Title: strings.TrimSpace(af.Title)}
	for _, l := range af.Links {
		switch {
		case l.Rel == "next":
			f.Next = c.resolve(base, l.Href)
		case strings.Contains(l.Rel, "crawlable"):
			f.Crawl = c.resolve(base, l.Href)
		case l.Rel == "search" && strings.Contains(l.Type, "atom"):
			f.Search = c.resolve(base, l.Href)
		}
	}
	for _, e := range af.Entries {
		var files []BookFile
		var cover, nav, navRel string
		for _, l := range e.Links {
			switch {
			case strings.HasPrefix(l.Rel, "http://opds-spec.org/acquisition"):
				if h := c.resolve(base, l.Href); h != "" {
					files = append(files, BookFile{Href: h, Type: mediaType(l.Type)})
				}
			case l.Rel == "http://opds-spec.org/image":
				cover = c.resolve(base, l.Href)
			case l.Rel == "http://opds-spec.org/image/thumbnail" && cover == "":
				cover = c.resolve(base, l.Href)
			case nav == "" && (l.Rel == "" || l.Rel == "subsection" || strings.HasPrefix(l.Rel, "http://opds-spec.org/sort") ||
				strings.HasPrefix(l.Rel, "http://opds-spec.org/") && !strings.Contains(l.Rel, "acquisition")) &&
				strings.Contains(l.Type, "atom"):
				nav, navRel = c.resolve(base, l.Href), l.Rel
			}
		}
		if len(files) == 0 {
			if nav != "" {
				f.Nav = append(f.Nav, navLink{Title: strings.TrimSpace(e.Title), Href: nav, Rel: navRel})
			}
			continue
		}
		sum := strings.TrimSpace(e.Summary)
		if sum == "" {
			sum = strings.TrimSpace(e.Content)
		}
		f.Entries = append(f.Entries, CatalogEntry{
			ID: strings.TrimSpace(e.ID), Title: strings.TrimSpace(e.Title), Authors: strings.Join(trimAll(e.Authors), ", "),
			Summary: clip(plainText(sum), 600), Updated: strings.TrimSpace(e.Updated), Cover: cover, Files: files,
		})
	}
	return f, nil
}

// ── OPDS 2 (JSON) ───────────────────────────────────────────────────────────

type o2Link struct {
	Rel   any    `json:"rel"`
	Href  string `json:"href"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type o2Pub struct {
	Metadata struct {
		Identifier  string `json:"identifier"`
		Title       any    `json:"title"`
		Author      any    `json:"author"`
		Description string `json:"description"`
		Modified    string `json:"modified"`
	} `json:"metadata"`
	Links  []o2Link `json:"links"`
	Images []o2Link `json:"images"`
}

type o2Feed struct {
	Metadata struct {
		Title any `json:"title"`
	} `json:"metadata"`
	Links        []o2Link `json:"links"`
	Navigation   []o2Link `json:"navigation"`
	Publications []o2Pub  `json:"publications"`
}

func (c *opdsClient) parseOPDS2(base string, body []byte) (*feed, error) {
	var of o2Feed
	if err := json.Unmarshal(body, &of); err != nil {
		return nil, errors.New(ui.ErrNotCatalog)
	}
	f := &feed{Title: jsonText(of.Metadata.Title)}
	hasRel := func(l o2Link, want string) bool {
		for _, r := range relList(l.Rel) {
			if r == want {
				return true
			}
		}
		return false
	}
	for _, l := range of.Links {
		switch {
		case hasRel(l, "next"):
			f.Next = c.resolve(base, l.Href)
		case hasRel(l, "search") && !strings.Contains(l.Href, "{"):
			f.Search = c.resolve(base, l.Href)
		}
	}
	for _, n := range of.Navigation {
		if h := c.resolve(base, n.Href); h != "" {
			f.Nav = append(f.Nav, navLink{Title: n.Title, Href: h, Rel: strings.Join(relList(n.Rel), " ")})
		}
	}
	for _, p := range of.Publications {
		var files []BookFile
		for _, l := range p.Links {
			for _, r := range relList(l.Rel) {
				if strings.HasPrefix(r, "http://opds-spec.org/acquisition") {
					if h := c.resolve(base, l.Href); h != "" {
						files = append(files, BookFile{Href: h, Type: mediaType(l.Type)})
					}
					break
				}
			}
		}
		if len(files) == 0 {
			continue
		}
		var cover string
		for _, im := range p.Images {
			if cover = c.resolve(base, im.Href); cover != "" {
				break
			}
		}
		id := strings.TrimSpace(p.Metadata.Identifier)
		title := jsonText(p.Metadata.Title)
		if id == "" {
			id = files[0].Href
		}
		f.Entries = append(f.Entries, CatalogEntry{
			ID: id, Title: title, Authors: jsonNames(p.Metadata.Author), Summary: clip(plainText(p.Metadata.Description), 600),
			Updated: p.Metadata.Modified, Cover: cover, Files: files,
		})
	}
	return f, nil
}

func relList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// jsonText reads a title that is a string or a map of language to string.
func jsonText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// jsonNames reads an author that is a string, an object with a name, or a list of either.
func jsonNames(v any) string {
	var names []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			names = append(names, t)
		case map[string]any:
			names = append(names, jsonText(t["name"]))
		case []any:
			for _, y := range t {
				walk(y)
			}
		}
	}
	walk(v)
	return strings.Join(trimAll(names), ", ")
}

// ── helpers ─────────────────────────────────────────────────────────────────

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mediaType(t string) string {
	if i := strings.Index(t, ";"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(strings.TrimSpace(t))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// plainText drops markup from a summary: servers send HTML or XHTML there.
func plainText(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ── discovery and crawl ─────────────────────────────────────────────────────

// findRoot tries the address as given, then the places book servers keep their catalog, and
// returns the first that answers with a feed.
func (c *opdsClient) findRoot(ctx context.Context) (string, *feed, error) {
	base := strings.TrimRight(c.origin.String(), "/")
	origin := c.origin.Scheme + "://" + c.origin.Host
	tries := []string{base, base + "/opds", origin + "/opds", origin + "/opds2", origin + "/opds/v2.0"}
	seen := map[string]bool{}
	var firstErr error
	for _, t := range tries {
		if seen[t] {
			continue
		}
		seen[t] = true
		f, err := c.fetchFeed(ctx, t)
		if err == nil {
			return t, f, nil
		}
		if errors.Is(err, errAuth) {
			return "", nil, err
		}
		if firstErr == nil || errors.Is(firstErr, errNotFound) {
			firstErr = err
		}
	}
	if firstErr == nil || errors.Is(firstErr, errNotFound) {
		return "", nil, errors.New(ui.ErrNoCatalogFound)
	}
	return "", nil, firstErr
}

// crawl reads the catalog under root: the server's crawlable feed when it names one, else the
// navigation entries most likely to hold every book (recent, all, alphabetical). It is bounded
// in pages and books, and the caller bounds it in time through ctx.
func (c *opdsClient) crawl(ctx context.Context, root string, rootFeed *feed) ([]CatalogEntry, bool, error) {
	pages := 0
	var out []CatalogEntry
	seen := map[string]bool{}
	truncated := false
	add := func(es []CatalogEntry) {
		for _, e := range es {
			if e.ID == "" || e.Title == "" || seen[e.ID] {
				continue
			}
			if len(out) >= maxCatalogLen {
				truncated = true
				return
			}
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	visited := map[string]bool{root: true}
	var walk func(f *feed, depth int) error
	walk = func(f *feed, depth int) error {
		add(f.Entries)
		for _, n := range listings(f.Nav, depth) {
			if depth >= 3 || visited[n.Href] || truncated {
				continue
			}
			visited[n.Href] = true
			if err := follow(ctx, c, n.Href, depth+1, &pages, walk); err != nil {
				return err
			}
		}
		for next := f.Next; next != "" && !visited[next] && !truncated; {
			visited[next] = true
			if pages >= maxFeedPages {
				truncated = true
				break
			}
			pages++
			nf, err := c.fetchFeed(ctx, next)
			if err != nil {
				return err
			}
			add(nf.Entries)
			for _, n := range listings(nf.Nav, depth) {
				if depth < 3 && !visited[n.Href] {
					visited[n.Href] = true
					if err := follow(ctx, c, n.Href, depth+1, &pages, walk); err != nil {
						return err
					}
				}
			}
			next = nf.Next
		}
		return nil
	}
	start := rootFeed
	if rootFeed.Crawl != "" && rootFeed.Crawl != root {
		visited[rootFeed.Crawl] = true
		cf, err := c.fetchFeed(ctx, rootFeed.Crawl)
		if err == nil {
			start = cf
		}
	}
	if err := walk(start, 0); err != nil && len(out) == 0 {
		return nil, false, err
	}
	return out, truncated, nil
}

func follow(ctx context.Context, c *opdsClient, href string, depth int, pages *int, walk func(*feed, int) error) error {
	if *pages >= maxFeedPages {
		return nil
	}
	*pages++
	f, err := c.fetchFeed(ctx, href)
	if err != nil {
		return err
	}
	return walk(f, depth)
}

// listings is the navigation worth following from a feed at depth: at the root only the
// listings of the whole library, below one of those every entry, because they split it up
// (a letter, a year).
func listings(nav []navLink, depth int) []navLink {
	if depth == 0 {
		return pickListings(nav)
	}
	return nav
}

// pickListings keeps the navigation entries that list books across the whole library. An
// author or tag index repeats what these already hold and would multiply the requests.
func pickListings(nav []navLink) []navLink {
	var first, second []navLink
	for _, n := range nav {
		t := strings.ToLower(n.Title)
		switch {
		case strings.Contains(n.Rel, "sort/new") || strings.Contains(t, "recent") || strings.Contains(t, "newest") || t == "new" || strings.Contains(t, "new books"):
			first = append(first, n)
		case t == "all books" || t == "books" || strings.Contains(t, "alphabetical") || strings.Contains(t, "all titles") || t == "titles" || t == "library":
			second = append(second, n)
		}
	}
	if len(second) > 0 {
		return second
	}
	return first
}
