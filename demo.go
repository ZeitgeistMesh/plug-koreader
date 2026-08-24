package main

// demo.go — the anonymous demo tier.
//
// The question was already asked and answered YES when this plug was scaffolded, which
// is what put this file here. What remains is to make the seeds honest.
//
// Seeds should demonstrate the product's RANGE, not merely be non-empty: a visitor
// should be able to learn what the plug does without clicking anything. Use
// controller/pkg/demo/persona so someone touring several plugs meets ONE person rather
// than unrelated fixtures.

import (
	"context"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
	"xf/controller/pkg/demo/persona"
)

// ensureDemoSeed lays down the persona's records the first time a demo workspace is
// read. It returns early once the workspace has rows, so a visitor who deletes a seeded
// record does not get it silently resurrected under them.
func (a *app) ensureDemoSeed(ctx context.Context, tk dblib.TenantKey) {
	if !demo.IsDemo(tk) {
		return
	}
	if _, ok := demo.NonceFromWorkspace(tk.Workspace); !ok {
		return
	}
	existing, err := a.items.List(ctx, tk)
	if err != nil || len(existing) > 0 {
		return
	}
	now := time.Now().UTC()
	seeds := []Book{
		// TODO: replace these with records that show what this plug is FOR. Two rows
		// that differ only in their timestamp teach a visitor nothing.
		{ID: "demo-1", Name: persona.Name + "'s first example", CreatedAt: now.AddDate(0, 0, -7).Format(time.RFC3339)},
		{ID: "demo-2", Name: "A second, different example", CreatedAt: now.AddDate(0, 0, -2).Format(time.RFC3339)},
	}
	for i := range seeds {
		seeds[i].Subject, seeds[i].Workspace = tk.Subject, tk.Workspace
	}
	_ = demo.Seed(ctx, a.items, tk, seeds, func(v Book) string { return v.ID })
}
