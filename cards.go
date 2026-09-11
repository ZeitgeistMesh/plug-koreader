package main

// cards.go — spaced repetition over the quotes you highlighted.
//
// # A CARD IS NOT A COPY OF A HIGHLIGHT
//
// The quote itself stays where it belongs: in the event the device sent, which is raw and
// re-syncable. A Card holds only the REVIEW STATE for one highlight id — when it is next
// due, how well it has been going. That split is what lets a re-sync overwrite every
// annotation without touching a review history, and lets an edited highlight keep its
// schedule. Store the derived thing separately from the thing it derives from.
//
// # THE ALGORITHM, AND WHY THIS ONE
//
// SM-2, the Anki family's ancestor, trimmed to what a reading app needs. It is chosen for
// being boring and legible: four grades, an ease factor that drifts with performance, and
// an interval that multiplies. Anything cleverer would be unexplainable when it produced a
// surprising due date, and a review queue nobody trusts is one nobody opens.
//
// Deliberately NOT modelled: sub-day intervals and a learning-steps ladder. A reading
// review is a thing you do once a day at most, so minutes-level scheduling would be
// precision the use case cannot express.

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
)

const collCards = "koreader_cards"

// Grade is the reviewer's own verdict. Four, not five, because the difference between
// "hard" and "barely" is not a judgement anyone makes reliably about a quote.
type Grade string

const (
	GradeForgot Grade = "forgot" // start again
	GradeHard   Grade = "hard"   // recalled, but it hurt
	GradeGood   Grade = "good"   // recalled
	GradeEasy   Grade = "easy"   // instant
)

func (g Grade) valid() bool {
	switch g {
	case GradeForgot, GradeHard, GradeGood, GradeEasy:
		return true
	}
	return false
}

// Card is the review state for one highlight.
type Card struct {
	// ID is the HIGHLIGHT's event id. Same key, different collection — so a card and
	// its quote are joined without storing the quote twice.
	ID string

	Ease     float64 // SM-2 ease factor, >= 1.3
	Interval int     // days until the next review
	Reps     int     // consecutive successful reviews
	Lapses   int

	DueOn      string // YYYY-MM-DD; due when this is today or earlier
	LastReview string
	Suspended  bool
}

const (
	minEase     = 1.3
	startEase   = 2.5
	maxInterval = 365 * 3
)

// newCard is an unseen quote: due immediately.
func newCard(id, today string) Card {
	return Card{ID: id, Ease: startEase, Interval: 0, DueOn: today}
}

// review applies a grade and returns the updated card.
//
// The shape of SM-2: a failure resets the interval and costs ease; a success multiplies
// the interval by the ease, which itself drifts up on easy and down on hard. The first two
// successes are given fixed intervals (1 then 6 days) because multiplying from zero would
// otherwise put a brand-new card a fortnight away on its first good answer.
func (c Card) review(g Grade, today time.Time) Card {
	if c.Ease < minEase {
		c.Ease = startEase
	}
	switch g {
	case GradeForgot:
		c.Lapses++
		c.Reps = 0
		c.Interval = 1
		c.Ease = math.Max(minEase, c.Ease-0.2)
	case GradeHard:
		c.Reps++
		c.Ease = math.Max(minEase, c.Ease-0.15)
		c.Interval = nextInterval(c.Reps, c.Interval, 1.2)
	case GradeGood:
		c.Reps++
		c.Interval = nextInterval(c.Reps, c.Interval, c.Ease)
	case GradeEasy:
		c.Reps++
		c.Ease += 0.15
		c.Interval = nextInterval(c.Reps, c.Interval, c.Ease*1.3)
	default:
		return c
	}
	if c.Interval > maxInterval {
		c.Interval = maxInterval
	}
	c.LastReview = today.Format("2006-01-02")
	c.DueOn = today.AddDate(0, 0, c.Interval).Format("2006-01-02")
	return c
}

func nextInterval(reps, current int, factor float64) int {
	switch reps {
	case 1:
		return 1
	case 2:
		return 6
	}
	n := int(math.Round(float64(current) * factor))
	if n <= current {
		n = current + 1
	}
	return n
}

// DueCard pairs a quote with its review state, which is what the review page renders.
type DueCard struct {
	Card      Card
	Highlight ReadEvent
	New       bool
}

// dueCards returns what is due today, newest-highlight-first among new ones and
// longest-overdue-first among seen ones.
//
// A highlight with no card yet is NEW and due immediately — that is how a fresh sync's
// quotes enter the queue without a separate "import" step that could fall out of date.
func (a *app) dueCards(ctx context.Context, tk dblib.TenantKey, idx *readingIndex, today string, limit int) ([]DueCard, int, error) {
	stored, err := a.cards.List(ctx, tk)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[string]Card, len(stored))
	for _, c := range stored {
		byID[c.ID] = c
	}

	var due []DueCard
	for _, h := range idx.Highlights() {
		c, seen := byID[h.ID]
		if seen && c.Suspended {
			continue
		}
		if !seen {
			due = append(due, DueCard{Card: newCard(h.ID, today), Highlight: h, New: true})
			continue
		}
		if c.DueOn <= today {
			due = append(due, DueCard{Card: c, Highlight: h})
		}
	}

	sort.SliceStable(due, func(i, j int) bool {
		// Overdue before new: a quote you have been carrying is worth more than one you
		// have never seen, and burying it under a fresh import is how a queue rots.
		if due[i].New != due[j].New {
			return !due[i].New
		}
		if due[i].New {
			return due[i].Highlight.StartTime > due[j].Highlight.StartTime
		}
		return due[i].Card.DueOn < due[j].Card.DueOn
	})

	total := len(due)
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, total, nil
}

// recordReview writes the new state for one card.
func (a *app) recordReview(ctx context.Context, tk dblib.TenantKey, id string, g Grade, loc *time.Location) error {
	id = strings.TrimSpace(id)
	if id == "" || !g.valid() {
		return nil
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	c, err := a.cards.Get(ctx, tk, id)
	if err != nil || c == nil {
		fresh := newCard(id, today)
		c = &fresh
	}
	updated := c.review(g, now)
	return a.cards.Put(ctx, tk, id, &updated)
}

// suspend takes a quote out of the rotation without deleting its history.
func (a *app) suspendCard(ctx context.Context, tk dblib.TenantKey, id string, loc *time.Location) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	today := time.Now().In(loc).Format("2006-01-02")
	c, err := a.cards.Get(ctx, tk, id)
	if err != nil || c == nil {
		fresh := newCard(id, today)
		c = &fresh
	}
	c.Suspended = true
	return a.cards.Put(ctx, tk, id, c)
}
