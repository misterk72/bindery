package importer

import (
	"os"
	"path/filepath"
	"strings"
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

// layoutEntry is one file or folder to create under a test root: a path
// ending in "/" is an empty folder, an .mp3 is an audio track, anything else
// a plain file.
func buildLayout(t *testing.T, root string, entries []string) []unmatchedScanFile {
	t.Helper()
	var audio []string
	for _, e := range entries {
		p := filepath.Join(root, e)
		if strings.HasSuffix(e, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if filepath.Ext(e) == ".mp3" {
			audio = append(audio, e)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return touchAudio(t, root, audio...)
}

// TestGroupUnmatched_DiscSetsFollowTheWalkerRule: a folder is one multi disc
// book only when every subfolder that holds audio is a CD, Disc or Disk
// folder. Wherever the call is unclear the books stay apart, because a set
// split in two can be adopted into one book, while separate books merged into
// one row cannot be adopted apart.
func TestGroupUnmatched_DiscSetsFollowTheWalkerRule(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		units   int
	}{
		{"Author/Book/CD1,CD2", []string{"Andy Weir/Artemis/CD1/01.mp3", "Andy Weir/Artemis/CD2/01.mp3"}, 1},
		{"Author/Book/Disc 01,02", []string{"Andy Weir/Artemis/Disc 01/01.mp3", "Andy Weir/Artemis/Disc 02/01.mp3"}, 1},
		{"Author/Book/Disk1,Disk2", []string{"Andy Weir/Artemis/Disk1/01.mp3", "Andy Weir/Artemis/Disk2/01.mp3"}, 1},
		{"Author/Series/Book/CD1,CD2", []string{"Brandon Sanderson/Mistborn/Artemis/CD1/01.mp3", "Brandon Sanderson/Mistborn/Artemis/CD2/01.mp3"}, 1},
		{"Author/Series/1,2", []string{"Brandon Sanderson/Mistborn/1/01.mp3", "Brandon Sanderson/Mistborn/2/01.mp3"}, 2},
		{"Root/1,2", []string{"1/01.mp3", "2/01.mp3"}, 2},
		{"Root/Book/CD1,CD2", []string{"Artemis/CD1/01.mp3", "Artemis/CD2/01.mp3"}, 1},
		{"Author/Book/CD1,CD2 + Artwork with only a jpg", []string{"Andy Weir/Artemis/CD1/01.mp3", "Andy Weir/Artemis/CD2/01.mp3", "Andy Weir/Artemis/Artwork/cover.jpg"}, 1},
		{"Author/Book/CD1,CD2 + empty hidden folder", []string{"Andy Weir/Artemis/CD1/01.mp3", "Andy Weir/Artemis/CD2/01.mp3", "Andy Weir/Artemis/.hidden/"}, 1},
		{"Author/Book/CD1,CD2 + Synology @eaDir", []string{"Andy Weir/Artemis/CD1/01.mp3", "Andy Weir/Artemis/CD2/01.mp3", "Andy Weir/Artemis/@eaDir/SYNOINDEX_MEDIA_INFO"}, 1},
		{"Author/Book/Part 1,2", []string{"Andy Weir/Artemis/Part 1/01.mp3", "Andy Weir/Artemis/Part 2/01.mp3"}, 2},
		{"series of numbered books", []string{"Brandon Sanderson/Mistborn/Book 1/01.mp3", "Brandon Sanderson/Mistborn/Book 2/01.mp3"}, 2},
		{"a subfolder with audio that is not a disc", []string{"Andy Weir/Artemis/CD1/01.mp3", "Andy Weir/Artemis/Extras/01.mp3"}, 2},
		{"Root/CD1,CD2 never merges the root", []string{"CD1/01.mp3", "CD2/01.mp3"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			groups, _ := groupUnmatched(buildLayout(t, root, tt.entries), []string{root})
			if len(groups) != tt.units {
				var paths []string
				for _, g := range groups {
					paths = append(paths, g.unit.RelPath)
				}
				t.Fatalf("units = %d %v, want %d", len(groups), paths, tt.units)
			}
			if tt.units == 1 && (groups[0].unit.ParsedTitle != "Artemis" || groups[0].unit.UnitKind != "folder") {
				t.Errorf("disc set unit = %+v, want a folder unit named after its book folder", groups[0].unit)
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
