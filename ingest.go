package main

// ingest.go — what the device sends, stored exactly as it sent it.
//
// # RAW EVENTS, NEVER PRE-AGGREGATED
//
// The device sends what happened; every derived number is computed here. Aggregate on
// the device and a bug fixed next month can never be re-derived over the history that
// was already collapsed — and the device is precisely where a clock is least
// trustworthy: an e-reader that has been asleep for a week comes back with the wrong
// time, and KOReader's own timestamps are the only ground truth we get.
//
// # IDEMPOTENT, BECAUSE THE DEVICE WILL RESEND
//
// These devices sync opportunistically over flaky wifi, get restored from backups, and
// retry whenever a reply did not arrive. Every event carries an id derived from its own
// content, so a resend overwrites itself and the count stays honest. This is free if it
// is decided now and very expensive later.
//
// # THE DEVICE PATH IGNORES Remote-* ENTIRELY
//
// The edge sends a Bearer request straight to the socket with `import stripidentity`,
// exactly as api.zm.is does for machine callers. This handler authenticates ONLY by the
// token: it never reads Remote-Email or Remote-Groups, so a forged identity header is
// inert even if one arrives.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"xf/controller/pkg/dblib"
)

const collEvents = "koreader_events"

// maxEventsPerSync bounds one request. A first sync from a device with years of history
// is genuinely large, so the device pages through rather than being refused — but an
// unbounded body is an unbounded allocation, and the edge caps the body anyway.
const maxEventsPerSync = 2000

// ReadEvent is one thing that happened, as the device reported it.
//
// The shape is deliberately close to KOReader's own: a per-page row with a start time and
// a duration is the whole of the reading statistics feature, and every number the product
// promises — pages a day, minutes a day, when you read, how fast — falls out of it.
type ReadEvent struct {
	// ID is derived from the event's CONTENT by the device: for a reading row, the
	// book, the page and the start time. Two syncs of the same row produce the same id,
	// which is what makes a resend harmless.
	ID   string
	Kind string // "read" | "highlight" | "bookmark"

	BookMD5 string // KOReader's own partial-file md5 — stable across renames
	Title   string
	Authors string
	Pages   int

	Page      int
	StartTime int64 // Unix seconds, UTC, as KOReader records it
	Duration  int   // seconds

	// Annotation half. Text is the user's own highlight; see the plan for why it is
	// stored but never shared through datashare and stays off MCP by default.
	Text    string `json:",omitempty"`
	Note    string `json:",omitempty"`
	Chapter string `json:",omitempty"`

	DeviceID   string
	ReceivedAt string
}

type ingestRequest struct {
	// What the device is, told to us on every sync rather than at pairing: an
	// e-reader gets upgraded, and the whole reason the first slice exists is to find
	// out which statistics schema a real device writes.
	KOReaderVersion string     `json:"koreader_version"`
	StatsSchema     string     `json:"stats_schema"`
	DeviceModel     string     `json:"device_model"`
	Events          eventBatch `json:"events"`
}

// eventBatch is []ReadEvent that also accepts `{}` for an empty batch.
//
// Lua has ONE table type and cannot distinguish an empty list from an empty map, so its
// JSON encoders emit `{}` for both unless the caller marks the table. A KOReader device
// with nothing new to report therefore sent `"events":{}`, and decoding refused the whole
// request with:
//
//	json: cannot unmarshal object into Go struct field ingestRequest.events of type []main.ReadEvent
//
// The cost of that refusal was not the lost events — there were none. It was the rest of
// the message: `koreader_version`, `stats_schema` and `device_model` travel on EVERY sync
// precisely so an empty one still proves the device is alive and says which schema it
// found, and that is the single most valuable thing this slice produces. A shape
// ambiguity in the client's language should not be able to delete it.
//
// The device-side marker is the real fix and is applied (zmsync's asArray). This is the
// other half: be exact about what is accepted. `[]` and `null` and `{}` mean an empty
// batch; a NON-EMPTY object is still an error, because that is a client sending something
// it has not thought through and silence would hide it.
type eventBatch []ReadEvent

func (b *eventBatch) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		*b = nil
		return nil
	}
	var events []ReadEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return err
	}
	*b = events
	return nil
}

type ingestReply struct {
	OK        bool `json:"ok"`
	Accepted  int  `json:"accepted"`
	Duplicate int  `json:"duplicate"`
	Rejected  int  `json:"rejected"`
	// HighWater is the largest StartTime this server has now stored for this device.
	// The device advances its cursor to what the SERVER acknowledged, never to what it
	// read — an acknowledgement that never arrived must cost a resend, not a gap.
	HighWater int64  `json:"high_water"`
	Device    string `json:"device"`
	Message   string `json:"message"`
}

// bearerToken pulls the token from the request. Two spellings because KOReader's HTTP
// client is a thin wrapper over luasocket and setting an Authorization header there is
// fiddlier than it should be; X-KOReader-Token is the escape hatch and is no weaker.
func bearerToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
		if lower := strings.ToLower(v); strings.HasPrefix(lower, "bearer ") {
			return strings.TrimSpace(v[len("bearer "):])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-KOReader-Token"))
}

// serveIngest accepts a batch of events from a paired device.
func (a *app) serveIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ingestReply{Message: "POST a batch of events"})
		return
	}
	dev, tk, ok := a.deviceForToken(r.Context(), bearerToken(r))
	if !ok {
		// 401 and a sentence, not a bare status. This is read on an e-ink screen by
		// somebody who has just typed a code, and "unauthorized" tells them nothing
		// about which of the two things went wrong.
		writeJSON(w, http.StatusUnauthorized, ingestReply{
			Message: "this device is not paired, or its access has been revoked — pair it again from the Devices page"})
		return
	}

	var req ingestRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ingestReply{Message: "could not read the batch: " + err.Error()})
		return
	}
	if len(req.Events) > maxEventsPerSync {
		writeJSON(w, http.StatusRequestEntityTooLarge, ingestReply{
			Message: fmt.Sprintf("%d events in one batch; send at most %d and page through", len(req.Events), maxEventsPerSync)})
		return
	}

	res := a.storeEvents(r.Context(), tk, dev, req.Events)
	res.Device = dev.Name

	// What the device told us about itself. Recorded on EVERY sync rather than once:
	// a firmware upgrade changes the statistics schema underneath us, and the first
	// symptom of that is a sync that silently stops finding rows.
	dev.LastSeenAt, dev.LastSyncAt = nowRFC(), nowRFC()
	if v := strings.TrimSpace(req.KOReaderVersion); v != "" {
		dev.KOReaderVersion = v
	}
	if v := strings.TrimSpace(req.StatsSchema); v != "" {
		dev.StatsSchema = v
	}
	if v := strings.TrimSpace(req.DeviceModel); v != "" {
		dev.DeviceModel = v
	}
	_ = a.devices.Put(r.Context(), tk, dev.ID, &dev)

	writeJSON(w, http.StatusOK, res)
}

// storeEvents writes a batch, counting what was new.
//
// A resend is a DUPLICATE, not an error: it is the normal consequence of a reply that did
// not arrive, and reporting it as a failure would teach the device to stop retrying —
// which is the one behaviour that actually loses data.
func (a *app) storeEvents(ctx context.Context, tk dblib.TenantKey, dev Device, events []ReadEvent) ingestReply {
	out := ingestReply{OK: true}
	for i := range events {
		ev := events[i]
		ev.ID = strings.TrimSpace(ev.ID)
		if ev.ID == "" || strings.TrimSpace(ev.Kind) == "" {
			// An event with no id cannot be deduplicated, so accepting it would make
			// every resend a new row and every count wrong. Counted and dropped.
			out.Rejected++
			continue
		}
		if existing, err := a.events.Get(ctx, tk, ev.ID); err == nil && existing != nil {
			out.Duplicate++
			if existing.StartTime > out.HighWater {
				out.HighWater = existing.StartTime
			}
			continue
		}
		ev.DeviceID = dev.ID
		ev.ReceivedAt = nowRFC()
		if err := a.events.Put(ctx, tk, ev.ID, &ev); err != nil {
			out.Rejected++
			continue
		}
		out.Accepted++
		if ev.StartTime > out.HighWater {
			out.HighWater = ev.StartTime
		}
	}
	// THE HIGH WATER IS THE SERVER'S, over everything stored — not just this batch.
	// A batch of pure duplicates would otherwise report a cursor lower than the device
	// already had, and walk it backwards into resending history forever.
	if hw := a.storedHighWater(ctx, tk, dev.ID); hw > out.HighWater {
		out.HighWater = hw
	}
	out.Message = fmt.Sprintf("%d new, %d already had, %d rejected", out.Accepted, out.Duplicate, out.Rejected)
	return out
}

// storedHighWater is the largest StartTime on record FOR THIS DEVICE.
//
// It was the largest for the whole TENANT, and that silently ate a seven-year backfill.
//
// The device pages through its history in ASCENDING start_time order, advancing its
// cursor to whatever this reply acknowledges. A tenant-wide maximum is not that device's
// progress — it is the newest row any device ever sent. So:
//
//	batch 1  → the oldest 500 rows (2019-12 …) are stored
//	reply    → high_water = max(this batch, EVERY other device's newest row)
//	batch 2  → "start_time > <that>", which is years ahead of where it had got to
//
// MEASURED on production 2026-09-11: one revoked synthetic device left behind by the
// bringup held a single event dated 2025-08-24. A real Kobo with 19,768 rows of history
// synced 3,219 of them — the first batch, then everything after that synthetic
// timestamp — and skipped roughly 16,000 rows in between. Nothing reported an error;
// the device said "Synced." each time, because from its side the server had acknowledged
// a cursor and it believed it.
//
// Per-device is the honest answer to "how far has THIS reader got", which is the only
// question the cursor is asking. The comment this replaces worried that a batch of pure
// duplicates would report a cursor LOWER than the device already held and walk it
// backwards — a real concern, and still handled, but on the device, where the cursor
// lives: zmsync never moves high_water down. Guarding it here cost more than it saved.
func (a *app) storedHighWater(ctx context.Context, tk dblib.TenantKey, deviceID string) int64 {
	all, err := a.events.List(ctx, tk)
	if err != nil {
		return 0
	}
	var hw int64
	for _, e := range all {
		if e.DeviceID != deviceID {
			continue
		}
		if e.StartTime > hw {
			hw = e.StartTime
		}
	}
	return hw
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
