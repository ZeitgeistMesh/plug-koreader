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
	// HighWater is the largest StartTime IN THIS BATCH (stored or already held). The
	// device advances its cursor to what the SERVER acknowledged, never to what it read —
	// an acknowledgement that never arrived must cost a resend, not a gap.
	//
	// IT IS NOT "THE LARGEST WE HAVE STORED", and that distinction ate a seven-year
	// backfill. The device pages ASCENDING, so the cursor has to be a CONTIGUOUS
	// watermark — "everything up to here has been sent" — and a maximum is not one. Any
	// single recent row already on the server (another device's, or this device's own
	// from an earlier broken run) makes the maximum jump years past where the paging had
	// actually got to, and every row in between becomes unreachable. Measured on
	// production 2026-09-11: 19,768 rows of history, 3,219 stored, no error anywhere.
	//
	// A batch maximum IS contiguous, because the device asks for `start_time > cursor`
	// ORDER BY start_time ASC — so everything below this batch's maximum has now been
	// offered. Two consequences that the device must handle, and does:
	//
	//   - an EMPTY batch reports 0. The device must not take that as "start again".
	//   - a retry of an OLD batch reports an old maximum.
	//
	// Both are the same rule, and it belongs on the device because that is where the
	// cursor lives: zmsync never moves high_water DOWN. Enforcing it here instead is what
	// produced the maximum-over-everything that caused the data loss.
	// OMITEMPTY IS LOAD-BEARING for an already-installed plugin. A batch maximum of 0
	// only happens for an empty batch, and the zmsync build currently on a device saves
	// whatever high_water it is handed — so sending an explicit 0 would reset that
	// device's cursor to the beginning on every heartbeat, making it re-send its whole
	// history for ever. Omitted, the field decodes as nil there and the old client skips
	// the save, which is the behaviour the new client gets from its forward-only guard.
	// A real start_time is never 0, so nothing legitimate is hidden by this.
	HighWater int64  `json:"high_water,omitempty"`
	Device    string `json:"device"`
	Message   string `json:"message"`
	// RejectReason is the first error behind a non-zero Rejected, so a count is never
	// reported on its own.
	RejectReason string `json:"reject_reason,omitempty"`
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
		sanitizeEventText(&ev)
		if err := a.events.Put(ctx, tk, ev.ID, &ev); err != nil {
			out.Rejected++
			// THE REASON TRAVELS. A bare "1184 rejected" is a number nobody can act on,
			// and it is what hid this for a day: the device reported a count, the count
			// looked like a rounding error, and 24 books were missing entirely.
			if out.RejectReason == "" {
				out.RejectReason = err.Error()
			}
			continue
		}
		out.Accepted++
		if ev.StartTime > out.HighWater {
			out.HighWater = ev.StartTime
		}
	}
	// HIGH WATER IS THIS BATCH'S MAXIMUM, and nothing wider. See the long note on
	// ingestReply.HighWater for why a "largest stored" watermark cannot work here.
	out.Message = fmt.Sprintf("%d new, %d already had, %d rejected", out.Accepted, out.Duplicate, out.Rejected)
	if out.RejectReason != "" {
		out.Message += " — " + out.RejectReason
	}
	return out
}

// sanitizeEventText scrubs the free text a device sends, because PostgreSQL cannot hold
// it verbatim and refuses the whole row when it cannot.
//
// MEASURED 2026-09-11: a real Kobo synced 19,768 rows and 18,584 arrived. The missing
// 1,185 belonged to exactly 24 books, and every one of those books lost ALL of its rows —
// never some. The books were PDFs, and KOReader reads their titles straight out of the
// PDF metadata, where they carry a trailing NUL:
//
//	title   = "Deep Work\x00"
//	authors = "Cal Newport\x00"
//
// jsonb has no representation for \u0000, so every Put for those books failed. The loss
// correlated perfectly — 24 of 24 missing books contained a control byte, 0 of 200 stored
// books did — which is what turned a plausible guess into a cause.
//
// Scrubbed here, at the STORE boundary, rather than on the device: this is a constraint of
// our storage, not a mistake by the client, and a second reader shipping a different
// KOReader build would otherwise rediscover it. Tabs and newlines survive because a
// highlight can legitimately contain them; everything else in C0, plus DEL, goes, and the
// result is forced to valid UTF-8 so a truncated multi-byte sequence cannot refuse a row
// either.
func sanitizeEventText(ev *ReadEvent) {
	for _, field := range []*string{&ev.Title, &ev.Authors, &ev.Text, &ev.Note, &ev.Chapter, &ev.Kind} {
		*field = scrubText(*field)
	}
}

func scrubText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
