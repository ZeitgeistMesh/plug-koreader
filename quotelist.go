package main

// quotelist.go — koreader.quote.list: your highlights, bookmarks and notes in one book,
// WITH their text.
//
// koreader.past.list names the book and keeps the passage out, because Past is a shared
// activity contract other plugs read. This is the other question — "what did I mark in
// this book?" — asked by the reader themselves, so the text is the answer. It is still
// scoped to the caller's own tenant and to koreader:user, and like every new verb it is
// invisible over MCP until the owner ticks it on mcp.zm.is/capabilities.
//
// THE BOOK IS FOUND BY TITLE, not by an id the caller cannot know: a case-insensitive
// substring of the title the device reported (with KOReader's "Unknown - " prefix
// ignored), or KOReader's own md5; an exact title wins over a substring. Two different
// books matching is refused with the
// candidates rather than guessed — the wrong book's quotes look exactly as plausible as
// the right one's. Left empty, it lists the annotated books, which is how to find a title.

import (
	"context"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/xats"
)

const capQuoteList = "koreader.quote.list"

const quoteListDefaultLimit = 500

func quoteListCapability() xats.Capability {
	return xats.Capability{
		Name: capQuoteList, Command: capQuoteList, Title: "Quotes in a Book",
		Description: "The highlights, bookmarks and notes you made in one book, with their text, in reading order " +
			"(page, then time). Name the book by part of its title (or KOReader's md5). Leave the book empty to list " +
			"the books you have annotated, with counts.",
		Icon: "fa-solid fa-quote-left", Emoji: "📖", Aliases: []string{"quotes", "highlights", "annotations"},
		RequiredScopes: []string{scopeUser}, ExposeToMCP: true,
		Security:    []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}},
		Annotations: map[string]any{"readOnlyHint": true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"book": map[string]any{"type": "string", "title": "Book",
				"description":   "Part of the book's title, or its md5. Leave it empty to list your annotated books.",
				"x-flow-prompt": "Which book? Part of its title is enough — leave it empty to see your annotated books."},
			"limit": map[string]any{"type": "number", "title": "How many",
				"description":   "How many quotes to return at most. Leave it empty for the default.",
				"x-flow-prompt": "How many at most? Leave it empty for the default."},
		}},
	}
}

// quoteBook is one annotated book in the no-book listing and in an ambiguity refusal.
type quoteBook struct {
	Title      string `json:"title"`
	Authors    string `json:"authors,omitempty"`
	MD5        string `json:"md5"`
	Highlights int    `json:"highlights"`
	Bookmarks  int    `json:"bookmarks"`
	Notes      int    `json:"notes"`
	LastAt     string `json:"last_annotated,omitempty"`
	lastAtUnix int64
}

type quoteItem struct {
	Kind    string `json:"kind"`
	Page    int    `json:"page,omitempty"`
	Chapter string `json:"chapter,omitempty"`
	Text    string `json:"text,omitempty"`
	Note    string `json:"note,omitempty"`
	At      string `json:"at,omitempty"`
}

func (a *app) handleQuoteList(ctx context.Context, caller string, meta map[string]any) map[string]any {
	tk := tenant(caller)
	idx, err := a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return map[string]any{"error": "could not read your reading", "body": "could not read your reading"}
	}
	loc := a.deviceLoc(ctx, tk)
	stamp := func(u int64) string {
		if u <= 0 {
			return ""
		}
		return time.Unix(u, 0).In(loc).Format(time.RFC3339)
	}

	books := map[string]*quoteBook{}
	for _, an := range idx.Annotations {
		b := books[an.BookMD5]
		if b == nil {
			b = &quoteBook{Title: displayTitle(an.Title), Authors: an.Authors, MD5: an.BookMD5}
			books[an.BookMD5] = b
		}
		switch an.Kind {
		case KindHighlight:
			b.Highlights++
		case KindBookmark:
			b.Bookmarks++
		}
		if strings.TrimSpace(an.Note) != "" {
			b.Notes++
		}
		if an.StartTime > b.lastAtUnix {
			b.lastAtUnix = an.StartTime
			b.LastAt = stamp(an.StartTime)
		}
	}
	list := func(only map[string]bool) []quoteBook {
		out := make([]quoteBook, 0, len(books))
		for md5, b := range books {
			if only == nil || only[md5] {
				out = append(out, *b)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].lastAtUnix != out[j].lastAtUnix {
				return out[i].lastAtUnix > out[j].lastAtUnix
			}
			return out[i].MD5 < out[j].MD5
		})
		return out
	}

	query := strings.TrimSpace(plugrun.Str(meta, "book"))
	if query == "" {
		all := list(nil)
		return map[string]any{"books": all, "count": len(all),
			"body": "Name a book to read its quotes; these are the books you have annotated."}
	}

	matched := map[string]bool{}
	if _, ok := books[query]; ok {
		matched[query] = true
	} else {
		// An exact title beats a substring, so "Dune" is not refused for also being in
		// "Dune Messiah".
		q := strings.ToLower(query)
		for md5, b := range books {
			if strings.ToLower(b.Title) == q {
				matched[md5] = true
			}
		}
		if len(matched) == 0 {
			for md5, b := range books {
				if strings.Contains(strings.ToLower(b.Title), q) {
					matched[md5] = true
				}
			}
		}
	}
	switch len(matched) {
	case 0:
		msg := "no annotated book matches " + quoteQ(query)
		return map[string]any{"error": msg, "body": msg, "books": list(nil)}
	case 1:
	default:
		msg := "more than one annotated book matches " + quoteQ(query) + " — name it more exactly, or by md5"
		return map[string]any{"error": msg, "body": msg, "books": list(matched)}
	}
	var md5 string
	for k := range matched {
		md5 = k
	}

	limit := quoteListDefaultLimit
	switch n := meta["limit"].(type) {
	case float64:
		if n > 0 {
			limit = int(n)
		}
	case int:
		if n > 0 {
			limit = n
		}
	}
	var evs []ReadEvent
	for _, an := range idx.Annotations {
		if an.BookMD5 == md5 {
			evs = append(evs, an)
		}
	}
	// Reading order, exactly as the quote reader pages through a book (quoteweb.go).
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Page != evs[j].Page {
			return evs[i].Page < evs[j].Page
		}
		return evs[i].StartTime < evs[j].StartTime
	})
	total := len(evs)
	if len(evs) > limit {
		evs = evs[:limit]
	}
	items := make([]quoteItem, 0, len(evs))
	for _, an := range evs {
		items = append(items, quoteItem{Kind: an.Kind, Page: an.Page, Chapter: an.Chapter,
			Text: an.Text, Note: an.Note, At: stamp(an.StartTime)})
	}
	b := books[md5]
	return map[string]any{"book": *b, "items": items, "count": len(items), "total": total}
}

func quoteQ(s string) string { return "\"" + s + "\"" }
