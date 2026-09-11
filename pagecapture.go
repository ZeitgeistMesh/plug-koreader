package main

// pagecapture.go — fetching the page a highlight came from.
//
// # WHY THIS IS A REQUEST QUEUE AND NOT A FETCH
//
// A highlight is a sentence out of context, and the context is on the device: the server
// has never seen the book and never should. But the device is an e-reader on a home
// network behind NAT, asleep most of the time, and the only direction that ever works is
// the device calling us.
//
// So a click cannot fetch anything. It RECORDS A REQUEST, the device picks it up on its
// next sync and posts the page back, and the page appears. The web side says so plainly
// rather than spinning, because "waiting for your reader" is the honest description and a
// spinner that resolves in four hours is a lie.
//
// # THE TEXT IS THE BOOK'S, NOT OURS
//
// A captured page is a page of a copyrighted book. It gets the same treatment the plan
// gave highlight text: stored because it is the user's own reading, never exposed through
// datashare, and off MCP — so an agent can know a page was captured without being handed
// the page.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"xf/controller/pkg/dblib"
)

const collPages = "koreader_pages"

// maxPageRequestsPerSync bounds how many pages a device is asked for at once. Each one
// costs opening a book and rendering a page on hardware with a single slow core, so a
// hundred queued requests must not turn one sync into a frozen reader.
const maxPageRequestsPerSync = 5

// PageCapture is one page of one book, requested from the web and filled by the device.
type PageCapture struct {
	// ID is "<book md5>:<page>" — content-addressed like every other id here, so two
	// requests for the same page are the same record and a re-fill overwrites itself.
	ID      string
	BookMD5 string
	Page    int
	Title   string

	// Status: "pending" until a device answers, then "done" or "failed".
	Status string
	Text   string
	// Error is why a capture failed, in the device's words. It is SHOWN. A page that
	// cannot be fetched must say what stopped it, or the button just looks broken —
	// which is how every other silent failure in this plug started.
	Error string

	RequestedAt string
	FilledAt    string
	Device      string
}

const (
	pageStatusPending = "pending"
	pageStatusDone    = "done"
	pageStatusFailed  = "failed"
)

func pageCaptureID(bookMD5 string, page int) string {
	return strings.TrimSpace(bookMD5) + ":" + strconv.Itoa(page)
}

// pendingPageRequest is what the ingest reply hands the device.
type pendingPageRequest struct {
	BookMD5 string `json:"book_md5"`
	Page    int    `json:"page"`
}

// pendingPageRequests returns the oldest pending captures, capped.
//
// OLDEST FIRST: a request that has waited through three syncs is the one most likely to be
// blocked on something, and putting newer ones in front of it for ever is how a queue
// grows a permanent floor.
func (a *app) pendingPageRequests(ctx context.Context, tk dblib.TenantKey) []pendingPageRequest {
	if a.pages == nil {
		return nil
	}
	all, err := a.pages.List(ctx, tk)
	if err != nil {
		return nil
	}
	pending := make([]PageCapture, 0, len(all))
	for _, p := range all {
		if p.Status == pageStatusPending {
			pending = append(pending, p)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].RequestedAt < pending[j].RequestedAt })
	if len(pending) > maxPageRequestsPerSync {
		pending = pending[:maxPageRequestsPerSync]
	}
	out := make([]pendingPageRequest, 0, len(pending))
	for _, p := range pending {
		out = append(out, pendingPageRequest{BookMD5: p.BookMD5, Page: p.Page})
	}
	return out
}

// requestPage records that the reader wants this page. Idempotent: asking twice for the
// same page is one request, and asking again for a FAILED one retries it.
func (a *app) requestPage(ctx context.Context, tk dblib.TenantKey, bookMD5, title string, page int) error {
	bookMD5 = strings.TrimSpace(bookMD5)
	if bookMD5 == "" || page <= 0 {
		return fmt.Errorf("a page request needs a book and a page")
	}
	id := pageCaptureID(bookMD5, page)
	if existing, err := a.pages.Get(ctx, tk, id); err == nil && existing != nil {
		if existing.Status == pageStatusDone {
			return nil // already have it
		}
		existing.Status = pageStatusPending
		existing.Error = ""
		existing.RequestedAt = nowRFC()
		return a.pages.Put(ctx, tk, id, existing)
	}
	return a.pages.Put(ctx, tk, id, &PageCapture{
		ID: id, BookMD5: bookMD5, Page: page, Title: title,
		Status: pageStatusPending, RequestedAt: nowRFC(),
	})
}

// pageCaptureFor reads one capture, or nil.
func (a *app) pageCaptureFor(ctx context.Context, tk dblib.TenantKey, bookMD5 string, page int) *PageCapture {
	if a.pages == nil || strings.TrimSpace(bookMD5) == "" || page <= 0 {
		return nil
	}
	p, err := a.pages.Get(ctx, tk, pageCaptureID(bookMD5, page))
	if err != nil {
		return nil
	}
	return p
}

// pageUpload is what the device posts back.
type pageUpload struct {
	BookMD5 string `json:"book_md5"`
	Page    int    `json:"page"`
	Text    string `json:"text"`
	// Error is the device's reason for not producing text. A capture that failed is
	// recorded as failed WITH the reason rather than left pending for ever, because a
	// request that can never succeed must stop being asked for.
	Error string `json:"error"`
}

// serveAPIPage receives a filled page from a paired device.
func (a *app) serveAPIPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "POST a page"})
		return
	}
	dev, tk, ok := a.deviceForToken(r.Context(), bearerToken(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"message": "this device is not paired, or its access has been revoked"})
		return
	}
	var up pageUpload
	// The same 8MB ceiling the event batch gets: a page of text is kilobytes, and an
	// unbounded body is an unbounded allocation.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&up); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "could not read the page: " + err.Error()})
		return
	}
	if strings.TrimSpace(up.BookMD5) == "" || up.Page <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "a page needs a book and a page number"})
		return
	}

	id := pageCaptureID(up.BookMD5, up.Page)
	cap := a.pageCaptureFor(r.Context(), tk, up.BookMD5, up.Page)
	if cap == nil {
		cap = &PageCapture{ID: id, BookMD5: up.BookMD5, Page: up.Page, RequestedAt: nowRFC()}
	}
	cap.Device = dev.Name
	cap.FilledAt = nowRFC()

	// SCRUBBED like every other string a device sends — a PDF page's text carries the
	// same control bytes its title does, and jsonb refuses the row for the same reason.
	text := scrubText(up.Text)
	if strings.TrimSpace(text) == "" {
		cap.Status = pageStatusFailed
		cap.Text = ""
		cap.Error = strings.TrimSpace(scrubText(up.Error))
		if cap.Error == "" {
			// An empty page with no reason is still a failure, and saying so is the
			// whole point. "Done, blank" would look like a book with an empty page.
			cap.Error = "the reader returned no text for this page"
		}
	} else {
		cap.Status = pageStatusDone
		cap.Text = text
		cap.Error = ""
	}
	if err := a.pages.Put(r.Context(), tk, id, cap); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "could not store the page: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": cap.Status})
}

// servePageRequest is the web button: "fetch this page from my reader".
func (a *app) servePageRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	page, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("page")))
	if err := a.requestPage(r.Context(), tenant(caller),
		strings.TrimSpace(r.FormValue("book")), strings.TrimSpace(r.FormValue("title")), page); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Back where they clicked from. The Referer is the only thing that knows whether the
	// button was on the day page or in the quote reader.
	back := strings.TrimSpace(r.Header.Get("Referer"))
	if back == "" {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
