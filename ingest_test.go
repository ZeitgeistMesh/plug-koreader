package main

// The properties that have to hold before a real device is pointed at this.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xf/controller/pkg/dblib"
)

func pairedDevice(t *testing.T, a *app) (Device, string, dblib.TenantKey) {
	t.Helper()
	tk := tenant("reader@zm.is")
	pc, err := newPairCode(tk)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.pairCodes.Put(context.Background(), pairCodeTenant(), pc.ID, &pc); err != nil {
		t.Fatal(err)
	}
	dev, token, err := a.claimPairCode(context.Background(), pc.ID, "Kobo Libra")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return dev, token, tk
}

// ── pairing ───────────────────────────────────────────────────────────────────

// SINGLE USE. A code claimable twice hands two devices the same person's identity.
func TestAPairCodeCannotBeClaimedTwice(t *testing.T) {
	a := testApp()
	tk := tenant("reader@zm.is")
	pc, _ := newPairCode(tk)
	_ = a.pairCodes.Put(context.Background(), pairCodeTenant(), pc.ID, &pc)

	if _, _, err := a.claimPairCode(context.Background(), pc.ID, "first"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, _, err := a.claimPairCode(context.Background(), pc.ID, "second"); err == nil {
		t.Fatal("the same code paired a second device")
	}
}

// A person reads the code off one screen and types it into another, at low contrast.
// Case and the grouping space must not matter.
func TestATypedCodeIsForgivingAboutCaseAndSpacing(t *testing.T) {
	a := testApp()
	pc, _ := newPairCode(tenant("reader@zm.is"))
	_ = a.pairCodes.Put(context.Background(), pairCodeTenant(), pc.ID, &pc)

	typed := strings.ToLower(pc.ID[:3]) + " " + strings.ToLower(pc.ID[3:])
	if _, _, err := a.claimPairCode(context.Background(), typed, "Kobo"); err != nil {
		t.Errorf("a correctly-read code typed with a space and in lower case was rejected: %v", err)
	}
}

// The alphabet must not contain a pair a reader cannot tell apart on e-ink.
func TestTheCodeAlphabetHasNoAmbiguousGlyphs(t *testing.T) {
	for _, bad := range []string{"0", "O", "1", "I", "L", "S", "5", "8", "B", "2", "Z"} {
		if strings.Contains(pairCodeAlphabet, bad) {
			t.Errorf("%q is in the pairing alphabet; it is confusable on a low-contrast screen", bad)
		}
	}
	if len(pairCodeAlphabet) < 20 {
		t.Errorf("the alphabet is %d characters, which makes a code too easy to guess", len(pairCodeAlphabet))
	}
}

// THE TOKEN IS NEVER STORED IN PLAINTEXT. We issue it and never need it back, so a
// stolen database must yield nothing that can sync.
func TestTheTokenIsStoredOnlyAsAHash(t *testing.T) {
	a := testApp()
	dev, token, tk := pairedDevice(t, a)

	stored, err := a.devices.Get(context.Background(), tk, dev.ID)
	if err != nil || stored == nil {
		t.Fatalf("device not stored: %v", err)
	}
	blob, _ := json.Marshal(stored)
	if strings.Contains(string(blob), token) {
		t.Fatal("the plaintext token is in the stored device record")
	}
	if stored.TokenSHA256 == "" || stored.TokenSHA256 == token {
		t.Errorf("TokenSHA256 = %q; it must be a digest, not the token", stored.TokenSHA256)
	}
	if len(stored.TokenTail) != 4 || !strings.HasSuffix(token, stored.TokenTail) {
		t.Errorf("TokenTail = %q; it should be the last four characters, for telling devices apart", stored.TokenTail)
	}

	ref, err := a.tokens.Get(context.Background(), pairCodeTenant(), hashToken(token))
	if err != nil || ref == nil {
		t.Fatal("no token reference, so no ingest could ever resolve this device")
	}
	refBlob, _ := json.Marshal(ref)
	if strings.Contains(string(refBlob), token) {
		t.Fatal("the plaintext token is in the token reference")
	}
}

// ── ingest ────────────────────────────────────────────────────────────────────

func ingest(t *testing.T, a *app, token string, body any) (*httptest.ResponseRecorder, ingestReply) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(raw))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.serveIngest(rec, r)
	var reply ingestReply
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec, reply
}

func sampleEvents(n int, base int64) []ReadEvent {
	out := make([]ReadEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ReadEvent{
			ID: "ev" + string(rune('a'+i)), Kind: "read",
			BookMD5: "abc", Title: "A Book", Page: i + 1,
			StartTime: base + int64(i*60), Duration: 45,
		})
	}
	return out
}

// IDEMPOTENT, because the device WILL resend: flaky wifi, restored backups, lost replies.
func TestResendingTheSameBatchAddsNothing(t *testing.T) {
	a := testApp()
	_, token, _ := pairedDevice(t, a)

	_, first := ingest(t, a, token, ingestRequest{Events: sampleEvents(3, 1_700_000_000)})
	if first.Accepted != 3 {
		t.Fatalf("first sync accepted %d, want 3", first.Accepted)
	}
	_, second := ingest(t, a, token, ingestRequest{Events: sampleEvents(3, 1_700_000_000)})
	if second.Accepted != 0 || second.Duplicate != 3 {
		t.Errorf("resend accepted=%d duplicate=%d, want 0 and 3 — a resend must overwrite itself",
			second.Accepted, second.Duplicate)
	}
}

// A RESEND IS NOT AN ERROR. Reporting one as failure teaches the device to stop
// retrying, which is the single behaviour that actually loses data.
func TestAResendIsReportedAsSuccess(t *testing.T) {
	a := testApp()
	_, token, _ := pairedDevice(t, a)
	_, _ = ingest(t, a, token, ingestRequest{Events: sampleEvents(2, 1_700_000_000)})
	rec, reply := ingest(t, a, token, ingestRequest{Events: sampleEvents(2, 1_700_000_000)})
	if rec.Code != http.StatusOK || !reply.OK {
		t.Errorf("a resend answered %d ok=%v", rec.Code, reply.OK)
	}
}

// AN EVENT WITH NO ID CANNOT BE DEDUPLICATED, so accepting it makes every resend a new
// row and every count wrong.
func TestAnEventWithNoIDIsRejectedRatherThanStored(t *testing.T) {
	a := testApp()
	_, token, tk := pairedDevice(t, a)
	_, reply := ingest(t, a, token, ingestRequest{Events: []ReadEvent{{Kind: "read", StartTime: 1}}})
	if reply.Rejected != 1 || reply.Accepted != 0 {
		t.Errorf("accepted=%d rejected=%d, want 0 and 1", reply.Accepted, reply.Rejected)
	}
	if all, _ := a.events.List(context.Background(), tk); len(all) != 0 {
		t.Errorf("%d events stored; an un-deduplicable event must not be kept", len(all))
	}
}

// THE HIGH WATER MUST NEVER GO BACKWARDS. A batch of pure duplicates would otherwise
// report a cursor lower than the device already had and walk it into resending forever.
func TestAnEmptyOrDuplicateBatchDoesNotRewindTheCursor(t *testing.T) {
	a := testApp()
	_, token, _ := pairedDevice(t, a)
	const base = int64(1_700_000_000)
	_, first := ingest(t, a, token, ingestRequest{Events: sampleEvents(3, base)})

	_, empty := ingest(t, a, token, ingestRequest{Events: nil})
	if empty.HighWater != first.HighWater {
		t.Errorf("an EMPTY sync moved the cursor from %d to %d", first.HighWater, empty.HighWater)
	}
	_, dup := ingest(t, a, token, ingestRequest{Events: sampleEvents(1, base)})
	if dup.HighWater < first.HighWater {
		t.Errorf("a duplicate batch rewound the cursor from %d to %d", first.HighWater, dup.HighWater)
	}
}

// An empty sync is how the server learns the device is alive and what schema it has —
// so it must be accepted, not refused as pointless.
func TestAnEmptySyncStillRecordsWhatTheDeviceIs(t *testing.T) {
	a := testApp()
	dev, token, tk := pairedDevice(t, a)
	_, _ = ingest(t, a, token, ingestRequest{
		KOReaderVersion: "v2026.04", StatsSchema: "page_stat_data(id_book,page,start_time,duration)",
		DeviceModel: "Kobo_libra", Events: nil,
	})
	stored, _ := a.devices.Get(context.Background(), tk, dev.ID)
	if stored == nil || stored.StatsSchema == "" || stored.KOReaderVersion == "" {
		t.Fatalf("the device's own report was not recorded: %+v", stored)
	}
	if stored.LastSyncAt == "" {
		t.Error("LastSyncAt was not stamped, so a silent device is indistinguishable from a busy one")
	}
}

// ── who may ingest ────────────────────────────────────────────────────────────

func TestIngestRefusesWithoutAToken(t *testing.T) {
	a := testApp()
	rec, reply := ingest(t, a, "", ingestRequest{Events: sampleEvents(1, 1)})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
	if !strings.Contains(reply.Message, "pair") {
		t.Errorf("the refusal does not say what to do about it: %q", reply.Message)
	}
}

// A REVOKED DEVICE STOPS IMMEDIATELY. Revocation that only takes effect on the next
// restart is not revocation.
func TestARevokedDeviceCannotIngest(t *testing.T) {
	a := testApp()
	dev, token, tk := pairedDevice(t, a)

	r := httptest.NewRequest(http.MethodPost, "/pair", nil)
	a.revokeDevice(r, tk, dev.ID)

	rec, _ := ingest(t, a, token, ingestRequest{Events: sampleEvents(1, 1_700_000_000)})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked device ingested (status %d)", rec.Code)
	}
}

// DEFENCE IN DEPTH, pinned per path. Revocation writes two records and deviceForToken
// checks either, so each one alone must be enough — otherwise a half-completed revoke
// leaves a device still syncing while the page says it is revoked.
func TestRevokingEitherRecordAloneStopsIngest(t *testing.T) {
	t.Run("token reference only", func(t *testing.T) {
		a := testApp()
		_, token, _ := pairedDevice(t, a)
		ref, _ := a.tokens.Get(context.Background(), pairCodeTenant(), hashToken(token))
		ref.Revoked = true
		_ = a.tokens.Put(context.Background(), pairCodeTenant(), ref.ID, ref)

		if rec, _ := ingest(t, a, token, ingestRequest{Events: sampleEvents(1, 1)}); rec.Code != http.StatusUnauthorized {
			t.Errorf("a revoked TOKEN still ingested (status %d)", rec.Code)
		}
	})
	t.Run("device only", func(t *testing.T) {
		a := testApp()
		dev, token, tk := pairedDevice(t, a)
		dev.Revoked = true
		_ = a.devices.Put(context.Background(), tk, dev.ID, &dev)

		if rec, _ := ingest(t, a, token, ingestRequest{Events: sampleEvents(1, 1)}); rec.Code != http.StatusUnauthorized {
			t.Errorf("a revoked DEVICE still ingested (status %d)", rec.Code)
		}
	})
}

// THE DEVICE PATH IGNORES Remote-*. The edge strips identity headers on the Bearer
// route; this asserts the handler does not read them even if one arrives.
func TestAForgedIdentityHeaderCannotIngest(t *testing.T) {
	a := testApp()
	_, _, tk := pairedDevice(t, a)

	raw, _ := json.Marshal(ingestRequest{Events: sampleEvents(1, 1_700_000_000)})
	r := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(raw))
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	rec := httptest.NewRecorder()
	a.serveIngest(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forged identity header was accepted (status %d)", rec.Code)
	}
	if all, _ := a.events.List(context.Background(), tk); len(all) != 0 {
		t.Errorf("%d events were written by an unauthenticated caller", len(all))
	}
}

// ── the addon archive ─────────────────────────────────────────────────────────

// KOReader loads `plugins/<name>.koplugin/`. An archive that unpacks bare files into
// `plugins/` produces no plugin AND NO ERROR — the single most likely way to get this
// wrong, and it is invisible until somebody wonders why the menu entry never appeared.
func TestTheAddonArchiveContainsThePluginDirectory(t *testing.T) {
	a := testApp()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/addon/"+addonDirName+".zip", nil)
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	a.serveAddonZip(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("the download is not a readable zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if !strings.HasPrefix(f.Name, addonDirName+"/") {
			t.Errorf("%q is not inside %s/ — unzipping this into plugins/ produces no plugin and no error",
				f.Name, addonDirName)
		}
	}
	for _, want := range []string{addonDirName + "/main.lua", addonDirName + "/_meta.lua"} {
		if !contains(names, want) {
			t.Errorf("%s is missing from the archive; KOReader needs both", want)
		}
	}
}

// The plugin's code must be READABLE before it is installed. A plugin asking for a
// device's whole reading history should not have to be unzipped to be inspected.
func TestThePluginSourceCanBeReadWithoutDownloading(t *testing.T) {
	a := testApp()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/addon/file/main.lua", nil)
	r.Header.Set("Remote-Email", "reader@zm.is")
	r.Header.Set("Remote-Groups", "plug_koreader")
	a.serveAddonFile(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ZmSync") {
		t.Error("that is not the plugin source")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q — the source should render, not download", ct)
	}
}

// A path outside the plugin directory must not be servable.
func TestThePluginFileRouteCannotEscapeItsDirectory(t *testing.T) {
	a := testApp()
	for _, attempt := range []string{"/addon/file/../../main.go", "/addon/file/%2e%2e/main.go"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, attempt, nil)
		r.Header.Set("Remote-Email", "reader@zm.is")
		r.Header.Set("Remote-Groups", "plug_koreader")
		a.serveAddonFile(rec, r)
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package main") {
			t.Errorf("%s served a file outside the plugin directory", attempt)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

var _ = io.Discard
