package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #2669. The chokepoint tests next door already prove no indexer is contacted
// when autoGrab.enabled is false. What they do not check, and what the bug
// report is actually about, is what the person who pressed the button is told:
// every one of these paths answered {"ok":true} and then dropped the search in
// a goroutine, so the button flashed and nothing happened.
//
// These tests assert the user visible outcome: the per ID entry is not ok, it
// carries the machine readable reason the web UI keys off, and it carries a
// message that names the setting. They deliberately use a mock searcher rather
// than a live scheduler, because the assertion is about the HTTP response, and
// "the searcher was never called" is the strongest local statement of "nothing
// was queued".

// refusalFixture is one author, two wanted books, a settings row for the kill
// switch, a mock searcher, and the bulk handler wired to all of it.
type refusalFixture struct {
	ctx      context.Context
	handler  *BulkHandler
	searcher *mockBookSearcher
	author   *models.Author
	books    []*models.Book
}

func newRefusalFixture(t *testing.T, autoGrab string) *refusalFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	settings := db.NewSettingsRepo(database)

	if err := settings.Set(ctx, "autoGrab.enabled", autoGrab); err != nil {
		t.Fatalf("seed autoGrab.enabled=%q: %v", autoGrab, err)
	}

	author := &models.Author{
		ForeignID: "OL_REFUSE_A", Name: "Refusal Author", SortName: "Author, Refusal",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("seed author: %v", err)
	}

	f := &refusalFixture{ctx: ctx, searcher: newMockBookSearcher(), author: author}
	for i := 1; i <= 2; i++ {
		b := &models.Book{
			ForeignID: fmt.Sprintf("OL_REFUSE_B%d", i), AuthorID: author.ID,
			Title: fmt.Sprintf("Wanted %d", i), SortTitle: fmt.Sprintf("wanted %d", i),
			Status: models.BookStatusWanted, Monitored: true,
			MetadataProvider: "openlibrary", Genres: []string{},
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatalf("seed book %d: %v", i, err)
		}
		f.books = append(f.books, b)
	}

	f.handler = NewBulkHandler(authors, books, db.NewBlocklistRepo(database), f.searcher).
		WithSeriesRepo(db.NewSeriesRepo(database)).
		WithSettingsRepo(settings).
		WithLifetimeCtx(ctx)
	return f
}

func (f *refusalFixture) bookIDs() []int64 {
	ids := make([]int64, 0, len(f.books))
	for _, b := range f.books {
		ids = append(ids, b.ID)
	}
	return ids
}

// decodeBulk reads the whole per ID envelope, not just one flag: the point of
// the fix is that the entry carries a reason as well as a false.
func decodeBulk(t *testing.T, rec *httptest.ResponseRecorder) bulkResponse {
	t.Helper()
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode bulk response: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

// assertRefused is the user visible contract, stated once.
func assertRefused(t *testing.T, resp bulkResponse, id int64) {
	t.Helper()
	got, ok := resp.Results[fmt.Sprintf("%d", id)]
	if !ok {
		t.Fatalf("no result entry for id %d; every requested id must be answered", id)
	}
	if got.OK {
		t.Fatalf("id %d reported ok:true with autoGrab.enabled=false; the user was told the search worked when nothing ran (#2669)", id)
	}
	if got.Code != autoGrabDisabledCode {
		t.Fatalf("id %d code = %q, want %q so the UI can show the message that names the setting", id, got.Code, autoGrabDisabledCode)
	}
	if got.Error == "" {
		t.Fatalf("id %d has no error text; an API client that does not localise gets nothing to show", id)
	}
}

func TestAuthorsBulkSearch_AutoGrabDisabled_RefusesVisibly(t *testing.T) {
	f := newRefusalFixture(t, "false")

	rec := postBulk(t, f.handler.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.author.ID))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: the refusal is reported per id, not as a transport error", rec.Code)
	}
	assertRefused(t, decodeBulk(t, rec), f.author.ID)
	// Nothing queued: the refusal has to be a refusal, not a warning
	// alongside a search that still runs.
	f.searcher.assertNoCall(t, 200*time.Millisecond)
}

func TestAuthorsBulkSearch_AutoGrabEnabled_StillQueues(t *testing.T) {
	// The control case. Without it the refusal assertion above would pass on a
	// handler that refuses everything.
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = 3 * time.Second })

	f := newRefusalFixture(t, "true")
	rec := postBulk(t, f.handler.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.author.ID))
	resp := decodeBulk(t, rec)
	if got := resp.Results[fmt.Sprintf("%d", f.author.ID)]; !got.OK || got.Code != "" {
		t.Fatalf("author result = %+v, want ok:true with no code when the switch is on", got)
	}
	f.searcher.waitForCall(t, 2*time.Second)
}

func TestWantedBulkSearch_AutoGrabDisabled_RefusesVisibly(t *testing.T) {
	f := newRefusalFixture(t, "false")
	ids := f.bookIDs()

	rec := postBulk(t, f.handler.WantedBulk, fmt.Sprintf(`{"ids":[%d,%d],"action":"search"}`, ids[0], ids[1]))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeBulk(t, rec)
	// Every selected book, not just the first: the Wanted page bulk bar acts
	// on a whole selection and the user must not have to guess which of them
	// ran.
	for _, id := range ids {
		assertRefused(t, resp, id)
	}
	f.searcher.assertNoCall(t, 200*time.Millisecond)
}

func TestWantedBulkSearch_AutoGrabEnabled_StillQueues(t *testing.T) {
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = 3 * time.Second })

	f := newRefusalFixture(t, "true")
	ids := f.bookIDs()
	rec := postBulk(t, f.handler.WantedBulk, fmt.Sprintf(`{"ids":[%d,%d],"action":"search"}`, ids[0], ids[1]))
	resp := decodeBulk(t, rec)
	for _, id := range ids {
		if got := resp.Results[fmt.Sprintf("%d", id)]; !got.OK || got.Code != "" {
			t.Fatalf("book %d result = %+v, want ok:true with no code when the switch is on", id, got)
		}
	}
	f.searcher.waitForCall(t, 2*time.Second)
}

func TestBooksBulkSearch_AutoGrabDisabled_RefusesVisibly(t *testing.T) {
	// The author page's multi select Search posts here rather than to
	// /author/bulk, so it needs the same treatment or half the author page
	// still lies.
	f := newRefusalFixture(t, "false")
	ids := f.bookIDs()

	rec := postBulk(t, f.handler.BooksBulk, fmt.Sprintf(`{"ids":[%d,%d],"action":"search"}`, ids[0], ids[1]))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeBulk(t, rec)
	for _, id := range ids {
		assertRefused(t, resp, id)
	}
	f.searcher.assertNoCall(t, 200*time.Millisecond)
}

// The switch is about grabbing, so it must not leak into the other bulk
// actions. Unmonitor with the switch off still succeeds.
func TestBulk_AutoGrabDisabled_LeavesOtherActionsAlone(t *testing.T) {
	f := newRefusalFixture(t, "false")
	ids := f.bookIDs()

	rec := postBulk(t, f.handler.WantedBulk, fmt.Sprintf(`{"ids":[%d],"action":"unmonitor"}`, ids[0]))
	resp := decodeBulk(t, rec)
	if got := resp.Results[fmt.Sprintf("%d", ids[0])]; !got.OK {
		t.Fatalf("unmonitor result = %+v with autoGrab.enabled=false, want ok:true; the switch gates grabbing only", got)
	}
}

// A handler built without a settings repo must behave exactly as it did before
// this change: fail open, queue the search. Several existing tests construct
// the handler that way.
func TestBulkSearch_NoSettingsRepo_FailsOpen(t *testing.T) {
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = 3 * time.Second })

	f := newRefusalFixture(t, "false")
	f.handler.settings = nil

	rec := postBulk(t, f.handler.WantedBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, f.books[0].ID))
	resp := decodeBulk(t, rec)
	if got := resp.Results[fmt.Sprintf("%d", f.books[0].ID)]; !got.OK {
		t.Fatalf("result = %+v with no settings repo, want ok:true (fail open)", got)
	}
	f.searcher.waitForCall(t, 2*time.Second)
}
