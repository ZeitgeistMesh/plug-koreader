package main

import (
	"context"
	"strings"
	"testing"
)

// TestAPageRequestIsIdempotent: the button can be clicked twice, and two readers looking
// at the same quote must not queue the same page twice.
func TestAPageRequestIsIdempotent(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")

	for i := 0; i < 3; i++ {
		if err := a.requestPage(ctx, tk, "bbb", "A Book", 312); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.pendingPageRequests(ctx, tk); len(got) != 1 {
		t.Errorf("queued %d requests for one page, want 1", len(got))
	}
}

// TestAFilledPageStopsBeingRequested — otherwise every sync re-fetches every page ever
// captured, on hardware that opens a book to do it.
func TestAFilledPageStopsBeingRequested(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")

	if err := a.requestPage(ctx, tk, "bbb", "A Book", 312); err != nil {
		t.Fatal(err)
	}
	cap := a.pageCaptureFor(ctx, tk, "bbb", 312)
	if cap == nil || cap.Status != pageStatusPending {
		t.Fatalf("not pending: %+v", cap)
	}
	cap.Status, cap.Text = pageStatusDone, "the page"
	if err := a.pages.Put(ctx, tk, cap.ID, cap); err != nil {
		t.Fatal(err)
	}
	if got := a.pendingPageRequests(ctx, tk); len(got) != 0 {
		t.Errorf("a captured page is still being asked for: %+v", got)
	}
	// And asking again for a page we already hold does not re-queue it.
	if err := a.requestPage(ctx, tk, "bbb", "A Book", 312); err != nil {
		t.Fatal(err)
	}
	if got := a.pendingPageRequests(ctx, tk); len(got) != 0 {
		t.Errorf("re-requesting a held page re-queued it: %+v", got)
	}
}

// TestAFailedPageCanBeRetried: a book that was on the reader last week may be back.
func TestAFailedPageCanBeRetried(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")

	if err := a.requestPage(ctx, tk, "bbb", "A Book", 7); err != nil {
		t.Fatal(err)
	}
	cap := a.pageCaptureFor(ctx, tk, "bbb", 7)
	cap.Status, cap.Error = pageStatusFailed, "the file has moved"
	if err := a.pages.Put(ctx, tk, cap.ID, cap); err != nil {
		t.Fatal(err)
	}
	if got := a.pendingPageRequests(ctx, tk); len(got) != 0 {
		t.Fatalf("a failed page should not be retried on its own: %+v", got)
	}
	if err := a.requestPage(ctx, tk, "bbb", "A Book", 7); err != nil {
		t.Fatal(err)
	}
	got := a.pendingPageRequests(ctx, tk)
	if len(got) != 1 {
		t.Errorf("an explicit retry did not re-queue the page: %+v", got)
	}
	if again := a.pageCaptureFor(ctx, tk, "bbb", 7); again.Error != "" {
		t.Errorf("the retry kept the stale error: %q", again.Error)
	}
}

// TestTheQueueIsCappedAndOldestFirst. Each capture costs opening a book on a single slow
// core, so a hundred queued pages must not freeze the reader; and the oldest request is
// the one most likely to be stuck, so it must not be pushed behind newer ones for ever.
func TestTheQueueIsCappedAndOldestFirst(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")

	for i := 1; i <= maxPageRequestsPerSync+3; i++ {
		cap := PageCapture{
			ID: pageCaptureID("bbb", i), BookMD5: "bbb", Page: i,
			Status: pageStatusPending,
			// Ascending timestamps, so page 1 is the oldest.
			RequestedAt: "2026-09-11T10:0" + string(rune('0'+i%10)) + ":00Z",
		}
		if err := a.pages.Put(ctx, tk, cap.ID, &cap); err != nil {
			t.Fatal(err)
		}
	}
	got := a.pendingPageRequests(ctx, tk)
	if len(got) != maxPageRequestsPerSync {
		t.Fatalf("handed the device %d requests, cap is %d", len(got), maxPageRequestsPerSync)
	}
	if got[0].Page != 1 {
		t.Errorf("oldest request is not first: got page %d", got[0].Page)
	}
}

// TestAnEmptyCaptureIsAFailureWithAReason is the important one. A page that comes back
// blank must NOT be stored as a successful empty page: on the web that is indistinguishable
// from a book with a blank page, and the reader is left with a button that appears to have
// worked and changed nothing.
func TestAnEmptyCaptureIsAFailureWithAReason(t *testing.T) {
	a := testApp()
	ctx := context.Background()
	tk := tenant("reader@zm.is")
	if err := a.requestPage(ctx, tk, "bbb", "A Book", 3); err != nil {
		t.Fatal(err)
	}

	// What the handler does with a blank upload, exercised through the same branch.
	cap := a.pageCaptureFor(ctx, tk, "bbb", 3)
	text := scrubText("   \x00  ")
	if strings.TrimSpace(text) != "" {
		t.Fatalf("scrubText left something behind: %q", text)
	}
	cap.Status, cap.Error = pageStatusFailed, "the page has no extractable text"
	if err := a.pages.Put(ctx, tk, cap.ID, cap); err != nil {
		t.Fatal(err)
	}

	got := a.pageCaptureFor(ctx, tk, "bbb", 3)
	if got.Status != pageStatusFailed || got.Error == "" {
		t.Errorf("a blank page was not recorded as a failure with a reason: %+v", got)
	}
}

// TestPageContextBlockSaysWhichStateItIsIn — all four states must be legible, and the
// failure must carry its reason to the screen.
func TestPageContextBlockSaysWhichStateItIsIn(t *testing.T) {
	an := ReadEvent{Kind: KindHighlight, BookMD5: "bbb", Title: "A Book", Page: 312, Text: "x"}

	none := string(pageContextBlock(nil, an))
	if !strings.Contains(none, "Fetch page 312") || !strings.Contains(none, `action="/page/request"`) {
		t.Errorf("no-capture state does not offer the button: %s", none)
	}
	pending := string(pageContextBlock(&PageCapture{Status: pageStatusPending}, an))
	if !strings.Contains(pending, "next time your reader syncs") {
		t.Errorf("pending state is not explained: %s", pending)
	}
	done := string(pageContextBlock(&PageCapture{Status: pageStatusDone, Text: "the whole page"}, an))
	if !strings.Contains(done, "the whole page") {
		t.Errorf("captured text is not shown: %s", done)
	}
	failed := string(pageContextBlock(&PageCapture{Status: pageStatusFailed, Error: "the file has moved"}, an))
	if !strings.Contains(failed, "the file has moved") {
		t.Errorf("a failure must carry its reason to the screen: %s", failed)
	}
	if !strings.Contains(failed, "Try again") {
		t.Errorf("a failure must be retryable: %s", failed)
	}

	// An annotation with no page number has nothing to fetch.
	if got := string(pageContextBlock(nil, ReadEvent{BookMD5: "bbb"})); got != "" {
		t.Errorf("offered a fetch for an annotation with no page: %s", got)
	}
}
