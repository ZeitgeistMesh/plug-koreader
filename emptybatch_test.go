package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xf/controller/pkg/webui"
)

// TestEmptyBatchFromALuaClient is the regression test for the failure a real Kobo hit on
// its first sync: LuaJSON cannot tell an empty list from an empty map, so the heartbeat
// arrived as `"events":{}` and the WHOLE request was refused —
//
//	json: cannot unmarshal object into Go struct field ingestRequest.events of type []main.ReadEvent
//
// The events were not the loss (there were none). The loss was koreader_version,
// stats_schema and device_model, which ride on every sync precisely so an empty one still
// reports what the device is.
func TestEmptyBatchFromALuaClient(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"lua empty table", `{"koreader_version":"v2026.03","stats_schema":"absent","device_model":"Kobo","events":{}}`},
		{"proper empty array", `{"koreader_version":"v2026.03","stats_schema":"absent","device_model":"Kobo","events":[]}`},
		{"null", `{"koreader_version":"v2026.03","stats_schema":"absent","device_model":"Kobo","events":null}`},
		{"omitted", `{"koreader_version":"v2026.03","stats_schema":"absent","device_model":"Kobo"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req ingestRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("decode failed: %v — an empty batch must still deliver the device report", err)
			}
			if len(req.Events) != 0 {
				t.Errorf("got %d events, want 0", len(req.Events))
			}
			if req.StatsSchema != "absent" || req.KOReaderVersion != "v2026.03" {
				t.Errorf("the device report did not survive: %+v", req)
			}
		})
	}
}

// TestNonEmptyBatchStillDecodes guards the other direction — the tolerance must not eat
// real events.
func TestNonEmptyBatchStillDecodes(t *testing.T) {
	body := `{"stats_schema":"page_stat_data","events":[
		{"ID":"a","Kind":"read","Page":3,"StartTime":1757000000,"Duration":41},
		{"ID":"b","Kind":"read","Page":4,"StartTime":1757000100,"Duration":22}]}`
	var req ingestRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(req.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(req.Events))
	}
	if req.Events[1].Page != 4 || req.Events[0].Duration != 41 {
		t.Errorf("events decoded wrong: %+v", req.Events)
	}
}

// TestNonEmptyObjectIsStillAnError keeps the tolerance NARROW. Accepting `{}` is a
// concession to a language that cannot say "empty list"; accepting `{"1":{...}}` would be
// guessing at a client that has genuinely sent the wrong shape, and swallowing that is how
// a silent partial ingest starts.
func TestNonEmptyObjectIsStillAnError(t *testing.T) {
	var req ingestRequest
	err := json.Unmarshal([]byte(`{"events":{"1":{"ID":"a"}}}`), &req)
	if err == nil {
		t.Fatal("a non-empty object decoded as an event batch; it must be refused")
	}
}

// TestDeviceRowsSelectableAndSearchable covers the list the devices page is built from:
// every row must carry the reserved select key (no key ⇒ no checkbox ⇒ nothing to bulk
// delete, which is the state the page was reported in), and search must actually narrow.
func TestDeviceRowsSelectableAndSearchable(t *testing.T) {
	devices := []Device{
		{ID: "d1", Name: "Kobo_daylight", CreatedAt: "2026-09-11", KOReaderVersion: "v2026.03"},
		{ID: "d2", Name: "Old Kindle", CreatedAt: "2026-08-01", KOReaderVersion: "v2025.11"},
	}

	rows, total := deviceRows(devices, webui.ListQuery{Page: 1, PageSize: 10})
	if total != 2 || len(rows) != 2 {
		t.Fatalf("got %d rows / total %d, want 2/2", len(rows), total)
	}
	for i, row := range rows {
		if got := string(row[webui.RowSelectKey]); got == "" {
			t.Errorf("row %d has no %s — it cannot be selected, so bulk delete cannot reach it", i, webui.RowSelectKey)
		}
	}
	// Newest first by default.
	if string(rows[0][webui.RowSelectKey]) != "d1" {
		t.Errorf("default order is not newest-first: got %q", rows[0][webui.RowSelectKey])
	}

	rows, total = deviceRows(devices, webui.ListQuery{Page: 1, PageSize: 10, Search: "kindle"})
	if total != 1 || len(rows) != 1 || string(rows[0][webui.RowSelectKey]) != "d2" {
		t.Fatalf("search did not narrow to the Kindle: %d rows, total %d", len(rows), total)
	}
}

// TestSchemaCellDoesNotWrap pins the fix for the reported "breaks into lines": the long
// schema string must be rendered on one line with the full value still reachable, rather
// than through codeCell's overflow-wrap:anywhere which shattered it per character.
func TestSchemaCellDoesNotWrap(t *testing.T) {
	const schema = "page_stat_data(id_book,page,start_time,duration,total_pages)"
	got := string(schemaCell(schema))
	if !strings.Contains(got, `class="ko-schema"`) {
		t.Errorf("schema cell is not using the no-wrap class: %s", got)
	}
	if !strings.Contains(got, `title="`) {
		t.Errorf("schema cell drops the full value on hover: %s", got)
	}
	if !strings.Contains(koSchemaCSS, "white-space:nowrap") {
		t.Error("the ko-schema rule no longer prevents wrapping")
	}
	// And the stacked/modal escape hatch must still let it wrap where there IS room.
	if !strings.Contains(koSchemaCSS, ".lv-detail .ko-schema") {
		t.Error("no override for the details panel — the full schema would stay ellipsised there")
	}
	if got := string(schemaCell("   ")); got != "—" {
		t.Errorf("blank schema should render an em dash, got %q", got)
	}
}

// TestHighWaterIsThisBatchNotEverythingStored is the regression test for a seven-year
// backfill that vanished with no error reported anywhere.
//
// The device pages ASCENDING and advances its cursor to what the reply acknowledges, so
// the cursor must be a CONTIGUOUS watermark. When high_water was "the largest StartTime
// stored", a single recent row already on the server dragged it years past where the
// paging had got to, and everything in between became unreachable.
//
// Measured on production 2026-09-11: 19,768 rows of real history, 3,219 ingested.
func TestHighWaterIsThisBatchNotEverythingStored(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	// A recent row already on the server. On production this came from a revoked
	// synthetic device; it is just as fatal when it is the device's OWN row from an
	// earlier broken run, which is why this test stores it against the same device.
	const recent = int64(1789147111) // 2026-09-11
	if err := a.events.Put(ctx, tk, "recent", &ReadEvent{
		ID: "recent", Kind: "read", DeviceID: dev.ID, StartTime: recent,
	}); err != nil {
		t.Fatal(err)
	}

	// The device sends its OLDEST batch, as a first sync does.
	const oldest = int64(1576518322) // 2019-12-16
	res := a.storeEvents(ctx, tk, dev, []ReadEvent{
		{ID: "e1", Kind: "read", StartTime: oldest},
		{ID: "e2", Kind: "read", StartTime: oldest + 60},
	})

	if res.Accepted != 2 {
		t.Fatalf("accepted %d, want 2", res.Accepted)
	}
	if res.HighWater != oldest+60 {
		t.Errorf("high_water = %d, want %d (the newest row in THIS batch).\n"+
			"Got %d? Then the cursor has jumped %d days past where the paging actually "+
			"reached, and every row in between is unreachable — silently, because the "+
			"device believes the acknowledgement.",
			res.HighWater, oldest+60, recent, (recent-(oldest+60))/86400)
	}
}

// TestEmptyBatchDoesNotClaimProgress pins the other half of the contract: a heartbeat
// proved nothing about any row, so it must report 0 and let the device hold its floor.
// (The device-side guard is in zmsync's syncNow: the cursor only ever moves forward.)
func TestEmptyBatchDoesNotClaimProgress(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	if err := a.events.Put(ctx, tk, "x", &ReadEvent{
		ID: "x", Kind: "read", DeviceID: dev.ID, StartTime: 1700000000,
	}); err != nil {
		t.Fatal(err)
	}
	res := a.storeEvents(ctx, tk, dev, nil)
	if res.HighWater != 0 {
		t.Errorf("an empty batch reported high_water = %d; it proved nothing about any "+
			"row and must report 0", res.HighWater)
	}
}

// TestDuplicateBatchStillReportsItsOwnMaximum: a resent batch is evidence that everything
// up to ITS maximum is held, which is exactly what the device should be told.
func TestDuplicateBatchStillReportsItsOwnMaximum(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	dev, _, tk := pairedDevice(t, a)

	const early, late = int64(1576518322), int64(1700000000)
	batch := []ReadEvent{{ID: "a", Kind: "read", StartTime: early}, {ID: "b", Kind: "read", StartTime: late}}
	if first := a.storeEvents(ctx, tk, dev, batch); first.HighWater != late {
		t.Fatalf("high_water = %d, want %d", first.HighWater, late)
	}
	again := a.storeEvents(ctx, tk, dev, batch)
	if again.Duplicate != 2 {
		t.Errorf("duplicate = %d, want 2", again.Duplicate)
	}
	if again.HighWater != late {
		t.Errorf("a resent batch reported %d, want %d — a duplicate is still proof the "+
			"row is held", again.HighWater, late)
	}
}
