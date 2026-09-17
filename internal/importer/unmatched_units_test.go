package importer

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func ebookFile(path string) unmatchedScanFile {
	return unmatchedScanFile{path: path, format: models.MediaTypeEbook, size: 100, mode: 0o644, title: filepath.Base(path)}
}

func audioFile(path, title string) unmatchedScanFile {
	return unmatchedScanFile{path: path, format: models.MediaTypeAudiobook, size: 1000, mode: 0o644, title: title}
}

// TestGroupUnmatched_Table pins where one book ends and the next begins.
func TestGroupUnmatched_Table(t *testing.T) {
	root := "/lib"
	type want struct {
		path, kind, format, folder, rel, title string
		files                                  int
	}
	tests := []struct {
		name  string
		files []unmatchedScanFile
		want  []want
	}{
		{
			name: "audio tracks in a book folder are one folder unit",
			files: []unmatchedScanFile{
				audioFile("/lib/Weir/Hail Mary/02.mp3", "Chapter 2"),
				audioFile("/lib/Weir/Hail Mary/01.mp3", "Project Hail Mary"),
				audioFile("/lib/Weir/Hail Mary/03.mp3", "Project Hail Mary"),
			},
			want: []want{{"/lib/Weir/Hail Mary", "folder", "audiobook", "Weir", "Weir/Hail Mary", "Project Hail Mary", 3}},
		},
		{
			name: "disc folders group under the book folder",
			files: []unmatchedScanFile{
				audioFile("/lib/Weir/Artemis/CD1/01.mp3", "Artemis"),
				audioFile("/lib/Weir/Artemis/Disc 2/01.mp3", "Artemis"),
			},
			want: []want{{"/lib/Weir/Artemis", "folder", "audiobook", "Weir", "Weir/Artemis", "Artemis", 2}},
		},
		{
			name: "a disc folder straight under the root is its own unit",
			files: []unmatchedScanFile{
				audioFile("/lib/CD1/01.mp3", "Something"),
				audioFile("/lib/CD1/02.mp3", "Something"),
			},
			want: []want{{"/lib/CD1", "folder", "audiobook", "CD1", "CD1", "Something", 2}},
		},
		{
			name: "loose audio at the root stands alone",
			files: []unmatchedScanFile{
				audioFile("/lib/a.m4b", "A"),
				audioFile("/lib/b.m4b", "B"),
			},
			want: []want{
				{"/lib/a.m4b", "file", "audiobook", "", "a.m4b", "A", 1},
				{"/lib/b.m4b", "file", "audiobook", "", "b.m4b", "B", 1},
			},
		},
		{
			name: "same stem ebooks are one book, other stems are separate",
			files: []unmatchedScanFile{
				ebookFile("/lib/Herbert/Dune.mobi"),
				ebookFile("/lib/Herbert/Dune.epub"),
				ebookFile("/lib/Herbert/Children of Dune.epub"),
			},
			want: []want{
				{"/lib/Herbert/Children of Dune.epub", "file", "ebook", "Herbert", "Herbert/Children of Dune.epub", "Children of Dune.epub", 1},
				{"/lib/Herbert/Dune.epub", "file", "ebook", "Herbert", "Herbert/Dune.epub", "Dune.epub", 2},
			},
		},
		{
			name: "an ebook beside audio is a separate book unit",
			files: []unmatchedScanFile{
				audioFile("/lib/X/Book/01.mp3", "Book"),
				ebookFile("/lib/X/Book/Book.epub"),
			},
			want: []want{
				{"/lib/X/Book", "folder", "audiobook", "X", "X/Book", "Book", 1},
				{"/lib/X/Book/Book.epub", "file", "ebook", "X", "X/Book/Book.epub", "Book.epub", 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups, truncated := groupUnmatched(tt.files, []string{root})
			if truncated {
				t.Fatal("unexpected truncation")
			}
			if len(groups) != len(tt.want) {
				t.Fatalf("got %d units, want %d: %+v", len(groups), len(tt.want), groups)
			}
			for i, w := range tt.want {
				u := groups[i].unit
				got := want{u.UnitPath, u.UnitKind, u.Format, u.AuthorFolder, u.RelPath, u.ParsedTitle, u.FileCount}
				if got != w {
					t.Errorf("unit %d = %+v, want %+v", i, got, w)
				}
				if len(u.MemberPaths) != u.FileCount || !slices.IsSorted(u.MemberPaths) {
					t.Errorf("unit %d members %v do not match its file count", i, u.MemberPaths)
				}
			}
		})
	}
}

// TestGroupUnmatched_Property is T2: for random trees, every file lands in
// exactly one unit and the units' file counts and sizes sum to the input.
func TestGroupUnmatched_Property(t *testing.T) {
	rng := rand.New(rand.NewPCG(2547, 1436))
	dirs := []string{"/lib", "/lib/A", "/lib/A/B1", "/lib/A/B1/CD1", "/lib/A/B1/CD2", "/lib/A/B2", "/lib/C", "/lib/C/Part 3", "/audio", "/audio/D/E"}
	stems := []string{"one", "two", "One", "three"}
	exts := []string{".epub", ".mobi", ".mp3", ".m4b", ".pdf"}
	for trial := range 300 {
		n := rng.IntN(60)
		seen := map[string]bool{}
		var files []unmatchedScanFile
		var wantSize int64
		for range n {
			p := filepath.Join(dirs[rng.IntN(len(dirs))], fmt.Sprintf("%s%d%s", stems[rng.IntN(len(stems))], rng.IntN(3), exts[rng.IntN(len(exts))]))
			if seen[p] {
				continue
			}
			seen[p] = true
			f := ebookFile(p)
			f.title = stems[rng.IntN(len(stems))]
			if detectDownloadFormat([]string{p}) == models.MediaTypeAudiobook {
				f.format = models.MediaTypeAudiobook
			}
			f.size = int64(rng.IntN(5000))
			wantSize += f.size
			files = append(files, f)
		}
		groups, _ := groupUnmatched(files, []string{"/lib", "/audio"})
		placed := map[string]int{}
		total := 0
		var size int64
		for _, g := range groups {
			total += g.unit.FileCount
			size += g.unit.SizeBytes
			for _, m := range g.unit.MemberPaths {
				placed[m]++
			}
		}
		if total != len(files) || size != wantSize {
			t.Fatalf("trial %d: units hold %d files / %d bytes, input %d / %d", trial, total, size, len(files), wantSize)
		}
		for _, f := range files {
			if placed[f.path] != 1 {
				t.Fatalf("trial %d: %s placed %d times", trial, f.path, placed[f.path])
			}
		}
	}
}

// TestEligibleUnmatched_SymlinkOutsideRoot is S10: a symlink inside the
// library never becomes a row, and neither does a file whose folder resolves
// outside every root.
func TestEligibleUnmatched_SymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.epub")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Author"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "Author", "Linked.epub")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkedDir := filepath.Join(root, "Elsewhere")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "Author", "Real.epub")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	in := []unmatchedScanFile{
		{path: link, format: models.MediaTypeEbook, mode: linkInfo.Mode()},
		{path: filepath.Join(linkedDir, "secret.epub"), format: models.MediaTypeEbook, mode: 0o644},
		{path: real, format: models.MediaTypeEbook, mode: 0o644},
	}
	got := eligibleUnmatched(in, []string{root})
	if len(got) != 1 || got[0].path != real {
		t.Fatalf("eligible = %+v, want only the real file", got)
	}
}

// TestScanLibrary_SymlinkedBookIsNotListed runs S10 through a real scan.
func TestScanLibrary_SymlinkedBookIsNotListed(t *testing.T) {
	s, _, _, _, _, libraryDir, ctx := unmatchedFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "Stolen.epub")
	writeEpubAt(t, secret, "", "", "")
	if err := os.MkdirAll(filepath.Join(libraryDir, "Someone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(libraryDir, "Someone", "Stolen.epub")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeEpubAt(t, filepath.Join(libraryDir, "Someone", "Honest.epub"), "", "", "")

	s.ScanLibrary(ctx)

	units := readUnmatchedFiles(t, ctx, s)
	if len(units) != 1 || filepath.Base(units[0].UnitPath) != "Honest.epub" {
		t.Fatalf("units = %+v, want only Honest.epub", units)
	}
}

// TestScanLibrary_RanksCandidatesFromTheCatalogue: a unit close to a book of
// its author, but below the reconcile's 0.85, is stored with that book as a
// suggestion. Only in memory catalogue rows are used.
func TestScanLibrary_RanksCandidatesFromTheCatalogue(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	var martian int64
	for _, title := range []string{"The Martian", "Artemis", "Project Hail Mary"} {
		b := &models.Book{ForeignID: "ol:" + title, AuthorID: author.ID, Title: title, Status: models.BookStatusWanted,
			MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if title == "The Martian" {
			martian = b.ID
		}
	}
	path := filepath.Join(libraryDir, "Andy Weir", "A Martyrs Tale", "A Martyrs Tale.epub")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, path, "", "", "")

	s.ScanLibrary(ctx)

	units := readUnmatchedFiles(t, ctx, s)
	if len(units) != 1 {
		t.Fatalf("units = %+v", units)
	}
	c := units[0].Candidates
	if len(c) == 0 || c[0].BookID != martian || c[0].Score < candidateThreshold || c[0].Score >= 0.85 {
		t.Fatalf("candidates = %+v, want The Martian first with a score in [0.60, 0.85)", c)
	}
	if units[0].AuthorFolder != "Andy Weir" || units[0].TopScore != c[0].Score {
		t.Errorf("unit = %+v", units[0])
	}
}

// countingTransport fails the test on any outbound request.
type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, fmt.Errorf("unexpected outbound request during a library scan")
}

// TestScanLibrary_MakesNoOutboundCalls is T5 for the scan: grouping and
// candidate ranking never reach a metadata provider.
func TestScanLibrary_MakesNoOutboundCalls(t *testing.T) {
	spy := &countingTransport{}
	prev := http.DefaultTransport
	http.DefaultTransport = spy
	t.Cleanup(func() { http.DefaultTransport = prev })

	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "ol:x", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := books.Create(ctx, &models.Book{ForeignID: "ol:aj", AuthorID: author.ID, Title: "Ancillary Justice",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Ann Leckie/Provenance.epub", "Nobody/Unknown.epub"} {
		p := filepath.Join(libraryDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEpubAt(t, p, "", "", "")
	}

	s.ScanLibrary(ctx)

	if spy.calls != 0 {
		t.Fatalf("outbound calls during scan = %d, want 0", spy.calls)
	}
	if n := len(readUnmatchedFiles(t, ctx, s)); n != 2 {
		t.Fatalf("units = %d, want 2", n)
	}
}

// TestAudioTrackedByFolder_DiscSet: adoption registers a disc set by its book
// folder, and the next scan must count every disc's tracks as tracked.
func TestAudioTrackedByFolder_DiscSet(t *testing.T) {
	tracked := map[string]bool{filepath.Clean("/lib/A/Book"): true}
	for _, p := range []string{"/lib/A/Book/01.mp3", "/lib/A/Book/CD1/01.mp3", "/lib/A/Book/Disc 2/05.mp3"} {
		if !audioTrackedByFolder(tracked, filepath.Clean(p)) {
			t.Errorf("%s not tracked", p)
		}
	}
	for _, p := range []string{"/lib/A/Other/01.mp3", "/lib/A/Book/Extras/01.mp3"} {
		if audioTrackedByFolder(tracked, filepath.Clean(p)) {
			t.Errorf("%s tracked, want untracked", p)
		}
	}
}

// BenchmarkGroupUnmatched is T6: grouping at the 50,000 file cap, a library
// of audiobook folders and loose ebooks.
func BenchmarkGroupUnmatched(b *testing.B) {
	files := make([]unmatchedScanFile, 0, maxUnmatchedFiles)
	for i := 0; len(files) < maxUnmatchedFiles; i++ {
		author := fmt.Sprintf("/lib/Author %03d", i%500)
		if i%2 == 0 {
			for track := range 20 {
				files = append(files, audioFile(fmt.Sprintf("%s/Book %05d/%02d.mp3", author, i, track), fmt.Sprintf("Book %05d", i)))
			}
		} else {
			files = append(files, ebookFile(fmt.Sprintf("%s/Book %05d.epub", author, i)))
		}
	}
	files = files[:maxUnmatchedFiles]
	roots := []string{"/lib"}
	b.ResetTimer()
	for b.Loop() {
		groups, _ := groupUnmatched(files, roots)
		if len(groups) == 0 {
			b.Fatal("no groups")
		}
	}
}
