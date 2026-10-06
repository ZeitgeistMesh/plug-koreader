package main

// quoteexport.go — koreader.quote.export and koreader.annotations.changed: how vision keeps
// its mirror of your highlights without a page ever waiting on this plug.
//
// Vision's "add a quote" browser shows every highlight, and its pages must answer from
// vision's own tables (memory never-wait-on-an-external-service-in-a-page-load). So vision
// keeps a MIRROR and refreshes it behind the page: it pulls this capability from a cursor,
// and it is told to pull by the event this plug publishes whenever a sync stores a new or
// changed annotation. Design: audit/designs/vision.md §1.3.
//
// THE CURSOR IS ReceivedAt, then the id. ReceivedAt is stamped on every store, including an
// overwrite (a note added on the device months later), so a changed highlight moves to the
// end of the order and the next pull sees it. The id breaks ties, because one sync stamps a
// whole batch in the same second, and a cursor of the time alone would skip half of a batch
// cut by the limit.
//
// Internal: this is a feed between two plugs, not a tool for a person, and it is not on MCP.
// Nothing calls it until vision (P1).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/xats"
)

const (
	capQuoteExport = "koreader.quote.export"

	// SubjectAnnotationsChanged is published (best effort) after a sync stores a new or
	// changed annotation. Not a command: a notification vision watches to pull its mirror.
	SubjectAnnotationsChanged = "xats.koreader.annotations.changed"

	quoteExportDefaultLimit = 500
	quoteExportMaxLimit     = 2000
)

// AnnotationsChangedEvent is the payload of SubjectAnnotationsChanged. It names whose
// highlights changed and nothing else; the subscriber pulls through its normal read path.
type AnnotationsChangedEvent struct {
	Subject   string `json:"subject"`
	Workspace string `json:"workspace"`
}

func quoteExportCapability() xats.Capability {
	return xats.Capability{
		Name: capQuoteExport, Command: capQuoteExport, Title: "Export Highlights",
		Description: "Every highlight with its text, in the order it arrived here, from a cursor. " +
			"For vision's mirror; not a person's tool.",
		RequiredScopes: []string{scopeUser},
		Internal:       true,
		Annotations:    map[string]any{"readOnlyHint": true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"after_cursor": map[string]any{"type": "string", "description": "The next_cursor of the previous page; empty for the start."},
			"limit":        map[string]any{"type": "number", "description": "How many at most (default 500, at most 2000)."},
		}},
	}
}

// exportItem is one highlight in the feed. Ref is the browser's spelling of its identity.
type exportItem struct {
	Ref         string `json:"ref"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	BookMD5     string `json:"book_md5"`
	Title       string `json:"title"`
	Authors     string `json:"authors,omitempty"`
	Chapter     string `json:"chapter,omitempty"`
	Page        int    `json:"page,omitempty"`
	Text        string `json:"text"`
	Note        string `json:"note,omitempty"`
	AnnotatedAt string `json:"annotated_at,omitempty"`
	ReceivedAt  string `json:"received_at"`
	cursor      string
}

func exportCursor(ev ReadEvent) string { return ev.ReceivedAt + "|" + ev.ID }

// quoteExport pages the caller's highlights after the cursor.
func quoteExport(annotations []ReadEvent, after string, limit int) (items []exportItem, next string, more bool) {
	if limit <= 0 {
		limit = quoteExportDefaultLimit
	}
	if limit > quoteExportMaxLimit {
		limit = quoteExportMaxLimit
	}
	all := make([]exportItem, 0, len(annotations))
	for _, ev := range annotations {
		if strings.TrimSpace(ev.Text) == "" {
			continue // a bookmark still waiting for its page is not a quote yet
		}
		it := exportItem{
			Ref: sourceKoreader + ":" + ev.ID, ID: ev.ID, Kind: ev.Kind, BookMD5: ev.BookMD5,
			Title: ev.Title, Authors: ev.Authors, Chapter: ev.Chapter, Page: ev.Page,
			Text: ev.Text, Note: ev.Note, ReceivedAt: ev.ReceivedAt, cursor: exportCursor(ev),
		}
		if ev.StartTime > 0 {
			it.AnnotatedAt = time.Unix(ev.StartTime, 0).UTC().Format(time.RFC3339)
		}
		if it.cursor > after {
			all = append(all, it)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].cursor < all[j].cursor })
	if len(all) > limit {
		all, more = all[:limit], true
	}
	next = after
	if len(all) > 0 {
		next = all[len(all)-1].cursor
	}
	return all, next, more
}

func (a *app) handleQuoteExport(ctx context.Context, caller string, meta map[string]any) map[string]any {
	if caller == "" {
		return map[string]any{"error": "not signed in", "body": "not signed in"}
	}
	tk := tenant(caller)
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return map[string]any{"error": "could not read your reading", "body": "could not read your reading"}
	}
	limit := 0
	if v, ok := meta["limit"].(float64); ok {
		limit = int(v)
	}
	items, next, more := quoteExport(idx.Annotations, strings.TrimSpace(plugrun.Str(meta, "after_cursor")), limit)
	return map[string]any{
		"items": items, "count": len(items), "next_cursor": next, "more": more,
		"body": "", // a feed, not prose
	}
}

// emitAnnotationsChanged tells subscribers this tenant's highlights changed. Best effort: a
// lost event is caught by vision's own refresh on its next page visit.
func (a *app) emitAnnotationsChanged(subject, workspace string) {
	if a.rt == nil || a.rt.Conn() == nil || strings.TrimSpace(subject) == "" {
		return
	}
	payload, err := json.Marshal(AnnotationsChangedEvent{Subject: subject, Workspace: workspace})
	if err != nil {
		return
	}
	_ = a.rt.Conn().Publish(SubjectAnnotationsChanged, payload)
}
