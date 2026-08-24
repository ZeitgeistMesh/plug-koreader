package main

import (
	"strings"
	"testing"

	"xf/controller/pkg/xats"
)

// TestVerifyContract mirrors the "advertises" check in mesh.verify. That check fails a
// component whose product capabilities do not reach BOTH the api and mcp gateways and
// carry a command word — and, separately, fails a component with NO product
// capabilities at all, on the grounds that a check which examined nothing did not pass.
//
// Running it here turns a release-time gate failure into a test failure, which is the
// difference between finding out in ten seconds and finding out on the box.
func TestVerifyContract(t *testing.T) {
	caps := testApp().manifest(testBase()).Capabilities
	if len(caps) == 0 {
		t.Fatal("no capabilities: plugrun registers one command handler per capability, so this plug would answer nothing")
	}

	product := 0
	for _, c := range caps {
		if c.Internal {
			continue
		}
		gated := len(c.RequiredScopes) > 0 || len(c.RequiresCapabilities) > 0 || c.ExposeToMCP
		if !gated {
			continue
		}
		product++
		for _, gw := range []string{xats.GatewayAPI, xats.GatewayMCP} {
			if !xats.ExposedToGateway(c, gw) {
				t.Errorf("capability %q is not exposed to the %q gateway; mesh.verify fails on this", c.Name, gw)
			}
		}
		if strings.TrimSpace(c.Command) == "" {
			t.Errorf("capability %q has no Command, so it is not reachable as a chat verb or an MCP tool", c.Name)
		}
	}
	if product == 0 {
		t.Fatal("no PRODUCT capabilities: every capability here is ungated, and mesh.verify treats that as a failure " +
			"rather than a pass — declare Scopes on the resource.Spec")
	}
}

// TestManifestIdentity covers what mesh.verify needs before it can check anything else:
// a manifest it can resolve, and a surface address to probe.
func TestManifestIdentity(t *testing.T) {
	m := testApp().manifest(testBase())
	if strings.TrimSpace(m.JID) == "" {
		t.Error("manifest has no JID; plugrun refuses to start without one")
	}
	if strings.TrimSpace(m.Name) == "" {
		t.Error("manifest has no Name")
	}
	if strings.TrimSpace(m.Title) == "" {
		t.Error("manifest has no Title; the hub and app grid render it")
	}
	if strings.TrimSpace(m.Subtitle) == "" {
		t.Error("manifest has no Subtitle; it is the app-grid card text")
	}
	if strings.TrimSpace(m.URL) == "" {
		t.Error("manifest has no URL, so front-ends render this plug as having no web surface " +
			"and mesh.verify records its surface as NOT CHECKED")
	}
}

// TestDispatchRejectsUnknownTyped is the "reachable" check's precondition. mesh.verify
// probes every capability with an EMPTY payload and treats a typed rejection as proof
// the path is wired; silence, or a reply that reads as "unknown capability", is how it
// detects one that is not.
func TestDispatchRejectsUnknownTyped(t *testing.T) {
	out := testApp().dispatch(t.Context(), "koreader.definitely-not-a-capability", map[string]any{})
	if out == nil {
		t.Fatal("dispatch returned nil for an unknown node; it must return a typed rejection")
	}
	if msg, _ := out["error"].(string); strings.TrimSpace(msg) == "" {
		t.Error("dispatch must reject an unknown node with an error string")
	}
}
