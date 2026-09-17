package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func touchAudio(t *testing.T, root string, rels ...string) []unmatchedScanFile {
	t.Helper()
	var out []unmatchedScanFile
	for _, rel := range rels {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, unmatchedScanFile{path: p, format: models.MediaTypeAudiobook, size: 1, mode: 0o644, title: filepath.Base(filepath.Dir(p))})
	}
	return out
}

// TestGroupUnmatched_DiscSetsFollowTheWalkerRule: only a folder whose
// subfolders are all disc folders is one book. Numbered books in a series
// folder, and numbered folders straight under an author, are separate books.
func TestGroupUnmatched_DiscSetsFollowTheWalkerRule(t *testing.T) {
	tests := []struct {
		name  string
		rels  []string
		units int
	}{
		{"series of numbered books stays apart", []string{
			"Brandon Sanderson/Mistborn/Book 1/01.mp3",
			"Brandon Sanderson/Mistborn/Book 2/01.mp3",
			"Brandon Sanderson/Mistborn/Book 3/01.mp3",
		}, 3},
		{"numbered folders under an author do not merge the author", []string{
			"Some Author/1/01.mp3",
			"Some Author/2/01.mp3",
		}, 2},
		{"a folder with a non disc subfolder is not a disc set", []string{
			"Andy Weir/Artemis/CD1/01.mp3",
			"Andy Weir/Artemis/Extras/01.mp3",
		}, 2},
		{"disc folders straight under a root stay apart", []string{
			"CD1/01.mp3",
			"CD2/01.mp3",
		}, 2},
		{"a real disc set is one book", []string{
			"Andy Weir/Artemis/CD1/01.mp3",
			"Andy Weir/Artemis/CD2/01.mp3",
			"Andy Weir/Artemis/Disc 3/01.mp3",
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			groups, _ := groupUnmatched(touchAudio(t, root, tt.rels...), []string{root})
			if tt.units == 1 && len(groups) == 1 && (groups[0].unit.ParsedTitle != "Artemis" || groups[0].unit.UnitKind != "folder") {
				t.Errorf("disc set unit = %+v, want a folder unit named after its book folder", groups[0].unit)
			}
			if len(groups) != tt.units {
				var paths []string
				for _, g := range groups {
					paths = append(paths, g.unit.RelPath)
				}
				t.Fatalf("units = %d %v, want %d", len(groups), paths, tt.units)
			}
		})
	}
}

// TestScanLibrary_TrackedFolderDoesNotHideALaterSibling: a registered audio
// folder must not make a numbered folder beneath it count as already tracked;
// a Book 4 added later is a new book for the scan to consider.
func TestScanLibrary_TrackedFolderDoesNotHideALaterSibling(t *testing.T) {
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "ol:bs", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	owner := &models.Book{ForeignID: "ol:mb", AuthorID: author.ID, Title: "Mistborn", Status: models.BookStatusWanted,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	series := filepath.Join(libraryDir, "Brandon Sanderson", "Mistborn")
	touchAudio(t, libraryDir, "Brandon Sanderson/Mistborn/01.mp3", "Brandon Sanderson/Mistborn/Book 4/01.mp3")
	if err := books.AddBookFile(ctx, owner.ID, models.MediaTypeAudiobook, series); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	b := readUnmatchedBlob(t, ctx, settings)
	if b.FilesFound != 2 || b.AlreadyTracked != 1 {
		t.Fatalf("counters = %+v, want the folder's own track tracked and Book 4 considered", b)
	}
}
