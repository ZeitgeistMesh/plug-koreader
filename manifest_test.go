package main

import (
	"strings"
	"testing"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/plugcfg"
)

// testApp and testBase are the seam the generated conformance test also uses. Keep
// their names: verify_contract_test.go is regenerated from the scaffold and calls them.
func testApp() *app {
	a := &app{
		items: dblib.NewFakeDocStore[Book](), webGroup: "plug_koreader", publicURL: "https://koreader.zm.is",
		// The reading rail. Part of the ONE fixture rather than a second constructor
		// beside it: two test apps drift, and the one that drifts is always the one
		// the failing test used.
		devices:   dblib.NewFakeDocStore[Device](),
		pairCodes: dblib.NewFakeDocStore[PairCode](),
		events:    dblib.NewFakeDocStore[ReadEvent](),
		tokens:    dblib.NewFakeDocStore[TokenRef](),
		cards:     dblib.NewFakeDocStore[Card](),
		pages:     dblib.NewFakeDocStore[PageCapture](),
		index:     newIndexCache(),
	}
	a.item = a.itemEntity()
	return a
}

func testBase() plugcfg.Base {
	return plugcfg.Base{ComponentName: "plug-koreader", ComponentJID: "koreader.zm.is", ComponentVersion: "test"}
}

// TestManifestContract pins the capability surface. The manifest — not this
// repository's HTTP routes — is what MCP, the chat router, docs.zm.is and the API are
// built from, so it is the contract worth asserting.
func TestManifestContract(t *testing.T) {
	m := testApp().manifest(testBase())
	if len(m.Capabilities) == 0 {
		t.Fatal("manifest has no capabilities — plugrun would register no command handlers")
	}
	for _, c := range m.Capabilities {
		if !strings.HasPrefix(c.Name, "koreader.") {
			t.Errorf("capability %q is outside this plug's namespace", c.Name)
		}
		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Description) == "" {
			t.Errorf("capability %q needs a Title and Description — they reach MCP, chat help and docs", c.Name)
		}
	}
	if m.JID != "koreader.zm.is" {
		t.Errorf("manifest JID = %q", m.JID)
	}
}

// TestTenantIsolation is the test every plug owes: one tenant's records must be
// invisible to another.
func TestTenantIsolation(t *testing.T) {
	a := testApp()
	mine := tenant("me@zm.is")
	theirs := tenant("you@zm.is")
	rec := Book{ID: "x1", Name: "mine"}
	if _, err := a.item.Spec().Save(t.Context(), mine, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := a.items.List(t.Context(), theirs)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("another tenant saw %d records", len(got))
	}
}

// TestDemoTierIsDeliberate pins the answer given when this plug was scaffolded. A demo
// tier is a product decision, and the failure mode it guards against is not someone
// changing it on purpose — it is someone changing it by accident and nobody noticing
// which way it went.
func TestDemoTierIsDeliberate(t *testing.T) {
	if !testApp().manifest(testBase()).Demo {
		t.Fatal("plug-koreader was scaffolded WITH a demo tier; if that is no longer true, " +
			"remove the demo gate, the seed and the demo_or_auth vhost together — an advertised " +
			"demo with no seed is worse than none")
	}
}
