package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// This file reads only the library.lastScan blob, so it builds and runs
// against the tree from before library adoption too. That is what records
// the fail before evidence for #2547 and the dropped 1000 file cap.

type unmatchedBlob struct {
	FilesFound     int               `json:"files_found"`
	Reconciled     int               `json:"reconciled"`
	Unmatched      int               `json:"unmatched"`
	AlreadyTracked int               `json:"already_tracked"`
	UnmatchedFiles []json.RawMessage `json:"unmatched_files"`
	UnmatchedUnits *int              `json:"unmatched_units"`
	UnitsTruncated bool              `json:"units_truncated"`
}

func readUnmatchedBlob(t *testing.T, ctx context.Context, settings *db.SettingsRepo) unmatchedBlob {
	t.Helper()
	setting, err := settings.Get(ctx, "library.lastScan")
	if err != nil || setting == nil {
		t.Fatalf("library.lastScan: %v %v", setting, err)
	}
	var b unmatchedBlob
	if err := json.Unmarshal([]byte(setting.Value), &b); err != nil {
		t.Fatalf("unmarshal scan result: %v", err)
	}
	return b
}

// TestScanLibrary_TrackFolderIsOneUnmatchedBook is #2547: a 193 track
// audiobook nobody has in the library is one book to decide about, not 193
// rows. The file counters keep their #1436 invariant (files_found equals
// reconciled + unmatched + already_tracked) because they count files.
func TestScanLibrary_TrackFolderIsOneUnmatchedBook(t *testing.T) {
	s, _, _, _, settings, libraryDir, ctx := unmatchedFixture(t)
	dir := filepath.Join(libraryDir, "Brandon Sanderson", "Rhythm of War")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 193; i++ {
		track := filepath.Join(dir, fmt.Sprintf("%03d - Rhythm of War.mp3", i))
		if err := os.WriteFile(track, buildID3v23(fmt.Sprintf("%03d-193", i), "Brandon Sanderson", ""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	b := readUnmatchedBlob(t, ctx, settings)
	if b.FilesFound != 193 || b.Unmatched != 193 || b.FilesFound != b.Reconciled+b.Unmatched+b.AlreadyTracked {
		t.Errorf("counters = %+v, want 193 files, all unmatched, invariant intact", b)
	}
	if len(b.UnmatchedFiles) != 0 {
		t.Errorf("unmatched_files has %d per file entries, want none: the list is units now", len(b.UnmatchedFiles))
	}
	if b.UnmatchedUnits == nil || *b.UnmatchedUnits != 1 {
		t.Errorf("unmatched_units = %v, want 1 book for the 193 tracks", b.UnmatchedUnits)
	}
}

// TestScanLibrary_UnmatchedListHasNoThousandCap: the old list stopped at 1000
// files and silently dropped the rest. 1200 unmatched books are 1200 units.
func TestScanLibrary_UnmatchedListHasNoThousandCap(t *testing.T) {
	s, _, _, _, settings, libraryDir, ctx := unmatchedFixture(t)
	dir := filepath.Join(libraryDir, "Unknown Author")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 1200 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("Book Number %04d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	b := readUnmatchedBlob(t, ctx, settings)
	if b.Unmatched != 1200 {
		t.Fatalf("unmatched = %d, want 1200", b.Unmatched)
	}
	if b.UnmatchedUnits == nil || *b.UnmatchedUnits != 1200 || b.UnitsTruncated {
		t.Errorf("unmatched_units = %v (truncated %v), want all 1200 stored; unmatched_files held %d",
			b.UnmatchedUnits, b.UnitsTruncated, len(b.UnmatchedFiles))
	}
}
