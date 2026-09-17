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
	if ok, _ := repo.ClaimState(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting); !ok {
		t.Fatal("claim failed")
	}
	if ok, err := repo.CompleteAdoption(ctx, u.ID, AdoptionRecord{BookID: book.ID}); err != nil || !ok {
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
