package main

// catalog.go — the saved copy of a person's book list, and the temporary cache of book files.
//
// A page never waits on the book server. It reads the saved catalog (one document per person)
// and, when that is old or missing, starts a refresh in the background and answers at once
// with what it has. Book files and covers are fetched only when asked for, into a directory
// that is capped in size and expires by age; nothing there is kept for good.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"xf/controller/pkg/agentprivacy"
	"xf/controller/pkg/dblib"
	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/xats"
)

const (
	collCatalog = "reader_catalog"
	catalogID   = "catalog"

	capCatalogList    = "reader.catalog.list"
	capCatalogRefresh = "reader.catalog.refresh"

	catalogMaxAge     = 6 * time.Hour
	catalogFailRetry  = 15 * time.Minute
	catalogCrawlLimit = 3 * time.Minute
)

// Catalog is the saved list of one person's books.
type Catalog struct {
	ID          string         `json:"id"`
	SourceURL   string         `json:"source_url"`
	Entries     []CatalogEntry `json:"entries"`
	RefreshedAt string         `json:"refreshed_at,omitempty"`
	TriedAt     string         `json:"tried_at,omitempty"`
	Error       string         `json:"error,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
	Subject     string         `json:"subject,omitempty"`
	Workspace   string         `json:"workspace,omitempty"`
}

func (a *app) loadCatalog(ctx context.Context, tk dblib.TenantKey) *Catalog {
	c, err := a.catalogs.Get(ctx, tk, catalogID)
	if err != nil || c == nil {
		return nil
	}
	return c
}

func (a *app) dropCatalog(ctx context.Context, tk dblib.TenantKey) {
	_ = a.catalogs.Delete(ctx, tk, catalogID)
}

// catalogDue says whether a refresh is worth starting: nothing saved, the saved list is for
// another server, it is old, or the last try failed a while ago.
func catalogDue(c *Catalog, s *Source, now time.Time) bool {
	if s == nil {
		return false
	}
	if c == nil || c.SourceURL != s.URL {
		return true
	}
	at := c.RefreshedAt
	retry := catalogMaxAge
	if c.Error != "" {
		at, retry = c.TriedAt, catalogFailRetry
	}
	t, err := time.Parse(time.RFC3339, at)
	return err != nil || now.Sub(t) > retry
}

// refreshSoon starts a refresh for the person unless one is already running.
func (a *app) refreshSoon(tk dblib.TenantKey) {
	key := tk.Workspace + "|" + tk.Subject
	a.refreshMu.Lock()
	if a.refreshing[key] {
		a.refreshMu.Unlock()
		return
	}
	a.refreshing[key] = true
	a.refreshMu.Unlock()
	go func() {
		defer func() {
			a.refreshMu.Lock()
			delete(a.refreshing, key)
			a.refreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), catalogCrawlLimit)
		defer cancel()
		a.refreshCatalog(ctx, tk)
	}()
}

func (a *app) isRefreshing(tk dblib.TenantKey) bool {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	return a.refreshing[tk.Workspace+"|"+tk.Subject]
}

// refreshCatalog reads the whole catalog from the server and replaces the saved one. A failed
// read keeps the old list and records the problem beside it.
func (a *app) refreshCatalog(ctx context.Context, tk dblib.TenantKey) {
	s := a.loadSource(ctx, tk)
	if s == nil {
		return
	}
	old := a.loadCatalog(ctx, tk)
	next := &Catalog{ID: catalogID, SourceURL: s.URL, TriedAt: nowRFC(), Subject: tk.Subject, Workspace: tk.Workspace}
	if old != nil && old.SourceURL == s.URL {
		next.Entries, next.RefreshedAt, next.Truncated = old.Entries, old.RefreshedAt, old.Truncated
	}
	c, err := a.clientFor(ctx, tk.Subject, s)
	if err == nil {
		var root string
		var rf *feed
		if root, rf, err = c.findRoot(ctx); err == nil {
			var entries []CatalogEntry
			var cut bool
			if entries, cut, err = c.crawl(ctx, root, rf); err == nil {
				sort.SliceStable(entries, func(i, j int) bool {
					return strings.ToLower(entries[i].Title) < strings.ToLower(entries[j].Title)
				})
				next.Entries, next.Truncated, next.RefreshedAt = entries, cut, nowRFC()
			}
		}
	}
	if err != nil {
		next.Error = err.Error()
		log.Printf("plug-reader: catalog refresh for %s: %v", tk.Subject, err)
	}
	if perr := a.catalogs.Put(ctx, tk, catalogID, next); perr != nil {
		log.Printf("plug-reader: catalog save for %s: %v", tk.Subject, perr)
	}
}

// searchCatalog filters entries by every word of the query against title and authors.
func searchCatalog(entries []CatalogEntry, query string) []CatalogEntry {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return entries
	}
	var out []CatalogEntry
	for _, e := range entries {
		hay := strings.ToLower(e.Title + " " + e.Authors)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

func entryKey(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:8])
}

func findEntry(c *Catalog, key string) *CatalogEntry {
	if c == nil {
		return nil
	}
	for i := range c.Entries {
		if entryKey(c.Entries[i].ID) == key {
			return &c.Entries[i]
		}
	}
	return nil
}

// ── capabilities ────────────────────────────────────────────────────────────

func catalogCapabilities() []xats.Capability {
	sec := []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}}
	return []xats.Capability{
		{
			Name: capCatalogList, Command: capCatalogList, Title: "Books on Your Server",
			Description: "The books saved from your connected book server, newest saved list first: title, authors and the formats on offer. " +
				"Optionally narrowed by a search over title and author. Reads the saved list, never the server.",
			Icon: "fa-solid fa-book", RequiredScopes: []string{scopeUser}, Security: sec, ExposeToMCP: true,
			Annotations: map[string]any{"readOnlyHint": true},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "words to find in a title or author"},
				"limit": map[string]any{"type": "integer", "description": "how many books to return, at most 200"},
			}},
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"books": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
					"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
					"authors": map[string]any{"type": "string"}, "formats": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				}}},
				"count": map[string]any{"type": "integer", "description": "how many books match"}, "refreshed_at": map[string]any{"type": "string"},
				"refreshing": map[string]any{"type": "boolean"},
			}},
		},
		{
			Name: capCatalogRefresh, Command: capCatalogRefresh, Title: "Refresh the Book List",
			Description: "Read the book list from the connected server again, in the background. The saved list stays in use until the new one is ready.",
			Icon:        "fa-solid fa-rotate", RequiredScopes: []string{scopeUser}, Security: sec, ExposeToMCP: false,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func (a *app) handleCatalogList(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	s := a.loadSource(ctx, tk)
	if s == nil {
		return errMap(ui.ErrConnectFirst)
	}
	c := a.loadCatalog(ctx, tk)
	if catalogDue(c, s, time.Now()) {
		a.refreshSoon(tk)
	}
	out := map[string]any{"books": []map[string]any{}, "count": 0, "refreshing": a.isRefreshing(tk)}
	if c == nil {
		return out
	}
	limit := 50
	if n, ok := meta["limit"].(float64); ok && n >= 1 {
		limit = int(min(n, 200))
	}
	hits := searchCatalog(c.Entries, plugrun.Str(meta, "query"))
	books := make([]map[string]any, 0, min(len(hits), limit))
	for _, e := range hits[:min(len(hits), limit)] {
		var fm []string
		for _, f := range e.Files {
			fm = append(fm, f.Type)
		}
		books = append(books, map[string]any{"id": entryKey(e.ID), "title": e.Title, "authors": e.Authors, "formats": fm})
	}
	out["books"], out["count"], out["refreshed_at"] = books, len(hits), c.RefreshedAt
	return out
}

func (a *app) handleCatalogRefresh(ctx context.Context, caller string, tk dblib.TenantKey, meta map[string]any) map[string]any {
	if agentprivacy.FromAgent(meta) {
		return errMap(ui.ErrAgentOnlyYou)
	}
	if a.loadSource(ctx, tk) == nil {
		return errMap(ui.ErrConnectFirst)
	}
	a.refreshSoon(tk)
	return map[string]any{"refreshing": true, "body": ui.ReadingLine}
}

// ── temporary file cache ────────────────────────────────────────────────────

// fileCache holds book files and covers fetched on demand. It is capped in total size and every
// file expires by age; eviction removes the oldest first.
type fileCache struct {
	dir     string
	maxSize int64
	ttl     time.Duration
	maxFile int64
	mu      sync.Mutex
}

func newFileCache(dir string, maxSize int64, ttl time.Duration, maxFile int64) *fileCache {
	return &fileCache{dir: dir, maxSize: maxSize, ttl: ttl, maxFile: maxFile}
}

func tenantDir(tk dblib.TenantKey) string {
	h := sha256.Sum256([]byte(tk.Workspace + "|" + tk.Subject))
	return hex.EncodeToString(h[:8])
}

func (f *fileCache) path(tk dblib.TenantKey, name string) string {
	return filepath.Join(f.dir, tenantDir(tk), name)
}

// fresh returns the cached file when it exists and has not expired.
func (f *fileCache) fresh(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && time.Since(st.ModTime()) < f.ttl
}

func (f *fileCache) dropTenant(tk dblib.TenantKey) {
	_ = os.RemoveAll(filepath.Join(f.dir, tenantDir(tk)))
}

// store writes the body to its place through a temporary file, refusing one over the size limit.
func (f *fileCache) store(p string, body io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(body, f.maxFile+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > f.maxFile {
		return errors.New(ui.ErrFileBig)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	f.evict()
	return nil
}

// evict removes expired files, then the oldest until the total is under the cap.
func (f *fileCache) evict() {
	f.mu.Lock()
	defer f.mu.Unlock()
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	_ = filepath.WalkDir(f.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		if time.Since(st.ModTime()) >= f.ttl {
			_ = os.Remove(p)
			return nil
		}
		items = append(items, item{p, st.Size(), st.ModTime()})
		total += st.Size()
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= f.maxSize {
			break
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

// janitor sweeps the cache on a timer until ctx ends.
func (f *fileCache) janitor(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		f.evict()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fileExt picks a file extension from the media type.
func fileExt(mediaType string) string {
	switch mediaType {
	case "application/epub+zip":
		return "epub"
	case "application/pdf":
		return "pdf"
	case "application/x-mobipocket-ebook", "application/vnd.amazon.mobi8-ebook":
		return "mobi"
	case "application/x-cbz", "application/vnd.comicbook+zip":
		return "cbz"
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	}
	if i := strings.LastIndex(mediaType, "/"); i >= 0 && i+1 < len(mediaType) {
		e := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, mediaType[i+1:])
		if e != "" && len(e) <= 8 {
			return e
		}
	}
	return "bin"
}

// fetchToCache makes sure the file is in the cache (fetching it when it is not or has expired)
// and returns its path.
func (a *app) fetchToCache(ctx context.Context, tk dblib.TenantKey, s *Source, name, href string) (string, error) {
	p := a.files.path(tk, name)
	if a.files.fresh(p) {
		return p, nil
	}
	c, err := a.clientFor(ctx, tk.Subject, s)
	if err != nil {
		return "", err
	}
	resp, err := c.get(ctx, href, "*/*")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := a.files.store(p, resp.Body); err != nil {
		return "", err
	}
	return p, nil
}

// serveBookFile streams one format of one book to its owner, fetched on demand.
func (a *app) serveBookFile(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	e := findEntry(c, r.URL.Query().Get("id"))
	if s == nil || e == nil {
		http.NotFound(w, r)
		return
	}
	idx := 0
	if n := r.URL.Query().Get("f"); n != "" {
		_, _ = fmt.Sscanf(n, "%d", &idx)
	}
	if idx < 0 || idx >= len(e.Files) {
		http.NotFound(w, r)
		return
	}
	f := e.Files[idx]
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p, err := a.fetchToCache(ctx, tk, s, entryKey(e.ID)+"-"+fmt.Sprint(idx)+"."+fileExt(f.Type), f.Href)
	if err != nil {
		http.Error(w, fill(ui.ErrFetchBook, "why", err.Error()), http.StatusBadGateway)
		return
	}
	name := safeFileName(e.Title) + "." + fileExt(f.Type)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if f.Type != "" {
		w.Header().Set("Content-Type", f.Type)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

// serveBookCover answers a cover image, fetched on demand; a missing one is a plain 404 and
// the list simply shows no picture.
func (a *app) serveBookCover(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	e := findEntry(c, r.URL.Query().Get("id"))
	if s == nil || e == nil || e.Cover == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := a.fetchToCache(ctx, tk, s, "cover-"+entryKey(e.ID)+".img", e.Cover)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Whatever the server called it, an image is only served as an image the browser may
	// sniff no further.
	head := make([]byte, 512)
	fh, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer fh.Close()
	n, _ := fh.Read(head)
	ct := http.DetectContentType(head[:n])
	if !strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = fh.Seek(0, io.SeekStart)
	_, _ = io.Copy(w, fh)
}

func safeFileName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "book"
	}
	return clip(s, 80)
}
