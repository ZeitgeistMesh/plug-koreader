package main

// entity.go declares this plug's owned entity ONCE. From this single declaration come
// the capabilities, the MCP tools, the chat verbs, the add wizard, the window-manager
// entity and the whole web CRUD surface — so this file is where the product lives.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/resource"
)

// koreaderApp is this plug's application identity — the name and logo its header shows
// on EVERY page. It is a required argument to resource.New and is passed to every
// entity, so the header cannot differ from route to route: there is one value, and
// nothing to keep in step. An entity's own Icon/Emoji is its glyph in the
// window-manager menu and never reaches the page header.
var koreaderApp = &resource.Identity{
	Name: "Koreader", Icon: "fa-solid fa-book-open", Emoji: "📖", IconColor: "#c9975c",
}

func (a *app) itemEntity() *resource.Resource[Book] {
	expose := true
	return resource.New(koreaderApp, resource.Spec[Book]{
		Name: "book", Prefix: "koreader", Group: a.webGroup,
		Title: "Book", Titles: "Book" + "s", Base: "/book",
		Description: "TODO: one line describing what this record is.",
		Icon:        "fa-solid fa-book-open", Emoji: "📖", IconColor: koreaderApp.IconColor,

		// Scopes are what make these PRODUCT capabilities. mesh.verify fails a
		// component whose capabilities are all ungated — "all zero of them advertise
		// everything" is true and worthless — so this is load-bearing, not decoration.
		Scopes:      []string{scopeUser},
		Security:    []map[string]any{{"type": "oauth2", "scopes": []any{scopeUser}}},
		ExposeToMCP: &expose,

		// Fields drive the add form, the chat wizard AND the capability InputSchema, so
		// they can never diverge. Keep Prompt SHORT and label-shaped: a plain field
		// renders Prompt AS its label, and explanation belongs in Placeholder.
		Fields: []resource.Field{
			{Name: "name", Label: "Book", Prompt: "Book", Required: true, Placeholder: "…"},
		},
		Columns: []resource.Col[Book]{
			{Header: "Book", Locked: true, Cell: func(v Book) string { return v.Name }},
			{Header: "Added", Cell: func(v Book) string { return v.CreatedAt }, Hidden: true, Priority: 3},
		},

		SearchPlaceholder: "Search…",
		EmptyText:         "Nothing yet — add the first one.",
		WebCreateLabel:    "Add " + strings.ToLower("Book"),
		// Only ever the caller's own rows, so SSO-group membership admits the list: a
		// lapsed entitlement should not hide someone their own data.
		WebListPrincipalGated: true,
		// ONE gate, used by every generated route — index, create page, add, delete,
		// autocomplete and the wizard. Without it a visitor gets a page they can read
		// and an action that 403s.
		WebAuthorize: a.webGate,

		Store: a.items,
		ID:    func(v Book) string { return v.ID },
		New: func(f map[string]string) (Book, error) {
			nm := strings.TrimSpace(f["name"])
			if nm == "" {
				return Book{}, fmt.Errorf("a name is required")
			}
			return Book{ID: newID(), Name: nm, CreatedAt: nowRFC()}, nil
		},
		Save: func(ctx context.Context, tk dblib.TenantKey, v Book) (string, error) {
			v.Subject, v.Workspace = tk.Subject, tk.Workspace
			if err := a.items.Put(ctx, tk, v.ID, &v); err != nil {
				return "", err
			}
			return "added " + v.Name, nil
		},
		Get: func(ctx context.Context, tk dblib.TenantKey, id string) (Book, bool, error) {
			v, err := a.items.Get(ctx, tk, strings.TrimSpace(id))
			if err != nil || v == nil {
				return Book{}, false, err
			}
			return *v, true, nil
		},
		Show: func(v Book) string { return v.Name },
		Sort: func(items []Book) {
			sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
		},
		// The demo workspace is seeded lazily on its first read, so an anonymous
		// visitor's first paint is already populated. A no-op for real tenants, and
		// it hangs off the read path every surface shares rather than off one page.
		List: func(ctx context.Context, tk dblib.TenantKey, _ map[string]any) ([]Book, error) {
			a.ensureDemoSeed(ctx, tk)
			return a.items.List(ctx, tk)
		},
	})
}
