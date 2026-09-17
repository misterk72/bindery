package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

func unitRepoOf(t *testing.T, s *Scanner) *db.UnmatchedUnitRepo {
	t.Helper()
	repo, ok := s.unmatchedUnits.(*db.UnmatchedUnitRepo)
	if !ok {
		t.Fatal("scanner has no unmatched unit repo wired")
	}
	return repo
}

func seedPendingUnit(t *testing.T, ctx context.Context, repo *db.UnmatchedUnitRepo, path string) {
	t.Helper()
	if _, err := repo.ReconcileScan(ctx, []db.UnmatchedUnitScan{{
		UnitPath: path, UnitKind: db.UnmatchedKindFile, Format: "ebook", FileCount: 1, MemberPaths: []string{path},
	}}, db.ReconcileScanOptions{SkipDeletion: true}); err != nil {
		t.Fatal(err)
	}
}

func pendingPaths(t *testing.T, ctx context.Context, repo *db.UnmatchedUnitRepo) map[string]bool {
	t.Helper()
	units, _, err := repo.List(ctx, db.UnmatchedListQuery{Limit: 250})
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool, len(units))
	for _, u := range units {
		out[u.UnitPath] = true
	}
	return out
}

// TestScanLibrary_ZeroFileScanKeepsUnits: a scan that finds no files at all,
// the unmounted volume case, leaves every stored unit alone.
func TestScanLibrary_ZeroFileScanKeepsUnits(t *testing.T) {
	s, _, _, _, _, libraryDir, ctx := unmatchedFixture(t)
	repo := unitRepoOf(t, s)
	kept := filepath.Join(libraryDir, "Gone", "Book.epub")
	seedPendingUnit(t, ctx, repo, kept)

	s.ScanLibrary(ctx)

	if !pendingPaths(t, ctx, repo)[kept] {
		t.Fatal("a scan that found no files removed a stored unit")
	}
}

// TestScanLibrary_TruncatedScanRemovesNothing: a scan over the unit cap did
// not see every unit, so a unit it did not list stays.
func TestScanLibrary_TruncatedScanRemovesNothing(t *testing.T) {
	prev := maxUnmatchedUnits
	maxUnmatchedUnits = 1
	t.Cleanup(func() { maxUnmatchedUnits = prev })

	s, _, _, _, _, libraryDir, ctx := unmatchedFixture(t)
	repo := unitRepoOf(t, s)
	beyond := filepath.Join(libraryDir, "Zeta", "Unlisted.epub")
	seedPendingUnit(t, ctx, repo, beyond)
	for _, rel := range []string{"Alpha/One.txt", "Beta/Two.txt"} {
		p := filepath.Join(libraryDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	if !pendingPaths(t, ctx, repo)[beyond] {
		t.Fatal("a truncated scan removed a unit it did not list")
	}
}
