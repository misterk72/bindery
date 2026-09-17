package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestReconcileScan_KeepsAdoptionsWhileTheirBookExists: an adopted unit's
// files are tracked, so no scan sees it again and its last_seen_at only ages.
// It must stay (Undo stays available) while its book exists, and go once the
// book is deleted.
func TestReconcileScan_KeepsAdoptionsWhileTheirBookExists(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "ol:a", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:b", AuthorID: author.ID, Title: "Provenance",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/p.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	u := unitByPath(t, database, repo, "/lib/A/p.epub")
	token, _ := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if token == "" {
		t.Fatal("claim failed")
	}
	if ok, err := repo.CompleteAdoption(ctx, u.ID, token, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET last_seen_at = ?`, unitTime(time.Now().Add(-90*24*time.Hour))); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/other.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/p.epub"); got == nil || got.State != UnmatchedStateAdopted {
		t.Fatalf("adoption of an existing book purged after 90 days: %+v", got)
	}

	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/other.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := unitByPath(t, database, repo, "/lib/A/p.epub"); got != nil {
		t.Fatalf("adoption whose book is gone kept: %+v", got)
	}
}

// TestReconcileScan_PurgesIgnoresUnderARemovedRoot: an old ignored row under a
// root that is no longer configured at all is purged, since no scan will see
// its files again; one under a configured root that found no files this time
// (an unmounted volume) is kept.
func TestReconcileScan_PurgesIgnoresUnderARemovedRoot(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	removed := scanUnit("/old-library/A/gone.epub")
	removed.RootPath = "/old-library"
	unmounted := scanUnit("/audiobooks/A/away.epub")
	unmounted.RootPath = "/audiobooks"
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{removed, unmounted}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE unmatched_units SET state = 'ignored', last_seen_at = ?`,
		unitTime(time.Now().Add(-40*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	res, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/new.epub")}, ReconcileScanOptions{
		RootsWithFiles: []string{"/lib"}, ConfiguredRoots: []string{"/lib", "/audiobooks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 1 {
		t.Fatalf("purged = %d, want only the row under the removed root", res.Purged)
	}
	if unitByPath(t, database, repo, "/old-library/A/gone.epub") != nil {
		t.Fatal("ignored row under a removed root kept")
	}
	if unitByPath(t, database, repo, "/audiobooks/A/away.epub") == nil {
		t.Fatal("ignored row under a configured root that found no files purged")
	}
}

// TestClaimToken_ScopesEveryWrite is the interleaving of two requests on one
// row: A claims it and stalls, recovery releases A's claim, B claims the row.
// Nothing A writes afterwards (progress, completion, reset) may land on B's
// claim.
func TestClaimToken_ScopesEveryWrite(t *testing.T) {
	ctx := context.Background()
	database, repo := openUnmatchedRepo(t)
	book := seedAdoptionBook(t, database)
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{scanUnit("/lib/A/x.epub")}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}
	id := unitByPath(t, database, repo, "/lib/A/x.epub").ID

	tokenA, _ := repo.Claim(ctx, id, UnmatchedStatePending, UnmatchedStateAdopting)
	if ok, _ := repo.ResetToPending(ctx, id, UnmatchedStateAdopting, tokenA); !ok {
		t.Fatal("recovery could not release A's claim")
	}
	tokenB, _ := repo.Claim(ctx, id, UnmatchedStatePending, UnmatchedStateAdopting)
	if tokenA == "" || tokenB == "" || tokenA == tokenB {
		t.Fatalf("tokens %q %q", tokenA, tokenB)
	}

	recA := AdoptionRecord{BookID: book.ID, Registered: []RegisteredFile{{Path: "/lib/A/x.epub", BookID: book.ID}}}
	if ok, _ := repo.RecordAdoptionProgress(ctx, id, tokenA, recA); ok {
		t.Error("A's progress landed on B's claim")
	}
	if ok, _ := repo.CompleteAdoption(ctx, id, tokenA, recA); ok {
		t.Error("A completed B's claim")
	}
	if ok, _ := repo.ResetToPending(ctx, id, UnmatchedStateAdopting, tokenA); ok {
		t.Error("A's failure path reset B's claim")
	}
	if ok, _ := repo.ReleaseClaim(ctx, id, UnmatchedStateAdopting, UnmatchedStatePending, tokenA); ok {
		t.Error("A released B's claim")
	}
	u, _ := repo.Get(ctx, id)
	if u.State != UnmatchedStateAdopting || u.ClaimToken != tokenB || len(u.Registered) != 0 {
		t.Fatalf("row after A's late writes = %+v, want B's untouched claim", u)
	}
	if ok, err := repo.CompleteAdoption(ctx, id, tokenB, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
		t.Fatalf("B's completion = %v %v", ok, err)
	}
}
