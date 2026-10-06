package main

// quotebrowse.go — the quote browser, which this plug no longer draws.
//
// /quotes (the searchable list) and /quote (the one-quote reader) are
// controller/pkg/quotebrowser, the ONE renderer behind every quote view in the mesh: this
// browser, vision's "add a quote" and vision's review. The owner asked for those three to
// share one implementation so they cannot drift (design: audit/designs/vision.md §3), so
// this file holds no markup at all. It says where the rows come from — the cached reading
// index, never a peer — and which controls exist.
//
// Star and Tag are on, and both are VISION's (annotate.go): a star makes a core memory,
// vision's own editable copy, and the highlight here is never written by anything but the
// device's own sync. The rows carry stars and tags from the cache of vision's answer, so
// the page never waits on vision.

import (
	"context"
	"strings"
	"time"

	"xf/controller/pkg/quotebrowser"
)

// sourceKoreader is the Ref source of a highlight: "koreader:<event id>".
const sourceKoreader = "koreader"

// koCorpus serves the caller's highlights from the cached reading index.
type koCorpus struct{ a *app }

func (c koCorpus) List(ctx context.Context, caller string) ([]quotebrowser.Quote, error) {
	tk := tenant(caller)
	loc := c.a.deviceLoc(ctx, tk)
	idx, err := c.a.readingIndexFor(ctx, caller, tk)
	if err != nil {
		return nil, err
	}
	anns, _ := c.a.annotationsFor(caller)
	hs := idx.Highlights()
	out := make([]quotebrowser.Quote, 0, len(hs))
	for _, h := range hs {
		q := highlightQuote(h, loc)
		if an, ok := anns[q.Ref.String()]; ok {
			q.Starred, q.Tags = an.Starred, an.Tags
		}
		out = append(out, q)
	}
	return out, nil
}

func (c koCorpus) Get(ctx context.Context, caller string, ref quotebrowser.Ref) (quotebrowser.Quote, bool, error) {
	all, err := c.List(ctx, caller)
	if err != nil {
		return quotebrowser.Quote{}, false, err
	}
	for _, q := range all {
		if q.Ref == ref {
			return q, true, nil
		}
	}
	return quotebrowser.Quote{}, false, nil
}

// highlightQuote is one highlight as the browser sees it. When it was marked links to that
// reading day, defined exactly as the calendar defines it.
func highlightQuote(h ReadEvent, loc *time.Location) quotebrowser.Quote {
	q := quotebrowser.Quote{
		Ref:  quotebrowser.Ref{Source: sourceKoreader, ID: h.ID},
		Text: h.Text, Note: h.Note,
		Book: displayTitle(h.Title), Author: strings.TrimSpace(h.Authors), Chapter: strings.TrimSpace(h.Chapter),
		BookKey: h.BookMD5, Page: h.Page,
	}
	if h.StartTime > 0 {
		q.At = time.Unix(h.StartTime, 0).In(loc)
		q.AtHref = "/day?d=" + readingDate(h.StartTime, loc)
	}
	return q
}

// quoteView is this plug's Config of the shared browser.
func (a *app) quoteView() quotebrowser.Config {
	reading := quotebrowser.ReadingOrder
	return quotebrowser.Config{
		ListPath: "/quotes", ReaderPath: "/quote",
		Corpus: koCorpus{a}, Annotator: koAnnotator{a},
		Star: true, Tag: true,
		// When vision's answer is not known yet, the controls say so instead of drawing a
		// state that may be wrong.
		Unavailable: a.annotationsUnavailable,
		// The list is newest first, because that is what a search wants; the reader pages
		// in reading order, so paging forward walks a book the way it was read.
		Order: quotebrowser.Newest, ReaderOrder: &reading,
		// /quote?id=<event id> links predate Refs; a bare id is a koreader highlight.
		DefaultSource: sourceKoreader,
		BooksPath:     "/books",
		ListTitle:     "Quotes", ReaderTitle: "Quote", ListLabel: "Every quote", SourceLabel: "Book",
		SearchPlaceholder: "Search your highlights…",
		EmptyTitle:        "No quotes yet",
		EmptyText: "Highlights are read from each book's sidecar on the device. Sync from ☰ → Tools → " +
			"Zeitgeist reading sync → Sync now and they will appear here.",
	}
}
