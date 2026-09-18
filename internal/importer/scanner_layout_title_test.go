package importer

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// effectiveScanTitle mirrors the three lines of ScanLibrary that decide which
// title a file is matched on, so the table below can cover a lot of layouts
// without building a library for each one.
func effectiveScanTitle(path, root, format string) (title, layout string) {
	parsed := parseScanFile(path, format)
	_, layout, _ = authorTitleFromLayout(path, root)
	return scanTitle(parsed.Title, layout, format), layout
}

// TestEffectiveScanTitle covers the layouts #2171 was reported on, plus the
// ones that must keep working. The book folder used to be taken as the title
// outright; it is now the fallback for an ebook, and still the leading signal
// for an audiobook whose files are per track names.
func TestEffectiveScanTitle(t *testing.T) {
	const root = "/lib"
	cases := []struct {
		name        string
		rel         string
		format      string
		wantTitle   string
		wantLayout  string
		description string
	}{
		{
			name:       "box set folder does not take the title from the file",
			rel:        "Robert Jordan/The Wheel of Time/01 - The Eye of the World (1990).epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "The Eye of the World",
			wantLayout: "The Wheel of Time",
		},
		{
			name:       "dotted position prefix",
			rel:        "Kandi Steiner/The Player/01. Make Me Yours.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "Make Me Yours",
			wantLayout: "The Player",
		},
		{
			// hoxtonia's layout, reported on #2171:
			// {authors}/<{series}/><{seriesIndex} - >{title}< ({year})>/...
			name:       "series index in the book folder is stripped",
			rel:        "Robert Jordan/The Wheel of Time/01 - The Eye of the World (1990)/The Eye of the World (1990).epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "The Eye of the World",
			wantLayout: "The Eye of the World",
		},
		{
			name:       "book folder names the book and the file does not",
			rel:        "Robert Jordan/The Wheel of Time/02 - The Great Hunt (1990)/ebook.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "ebook",
			wantLayout: "The Great Hunt",
		},
		{
			// nick7129's file, reported on #2171: a long shared authors folder,
			// a box set folder, and "{position}. {title} - {authors} ({year})".
			name:       "box set file naming its authors after the title",
			rel:        "Devney Perry, Amo Jones, Chelle Bliss/The Player/01. Make Me Yours - Devney Perry, Amo Jones, Chelle Bliss (2020).epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "Make Me Yours",
			wantLayout: "The Player",
		},
		{
			name:       "the folder IS the title",
			rel:        "Andy Weir/Project Hail Mary/Project Hail Mary.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "Project Hail Mary",
			wantLayout: "Project Hail Mary",
		},
		{
			name:       "flat author folder",
			rel:        "Andy Weir/Project Hail Mary.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "Project Hail Mary",
			wantLayout: "",
		},
		{
			name:       "a file with no title of its own falls back to the folder",
			rel:        "Andy Weir/Project Hail Mary/01.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "01",
			wantLayout: "Project Hail Mary",
		},
		{
			name:       "audiobook track keeps the folder title",
			rel:        "Andy Weir/Project Hail Mary/04 - Sinister Grey Mists.mp3",
			format:     models.MediaTypeAudiobook,
			wantTitle:  "Project Hail Mary",
			wantLayout: "Project Hail Mary",
		},
		{
			// A flat audiobook layout has no book folder for scanTitle to
			// prefer, so the position strip alone would decide, and it would
			// hand the scan a chapter name to match confidently. The strip is
			// gated on the ebook format for exactly this.
			name:       "flat audiobook track is parsed as it always was",
			rel:        "Andy Weir/01 - Rocky.mp3",
			format:     models.MediaTypeAudiobook,
			wantTitle:  "01",
			wantLayout: "",
		},
		{
			name:       "a title that opens with a year keeps it",
			rel:        "George Orwell/1984 - George Orwell.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "1984",
			wantLayout: "",
		},
		{
			name:       "a zero padded four digit position is still a position",
			rel:        "Andy Weir/Project Hail Mary/0001 - Project Hail Mary.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "Project Hail Mary",
			wantLayout: "Project Hail Mary",
		},
		{
			// The known trade this PR makes, pinned so it is a decision and
			// not a surprise: where the FOLDER is authoritative and the
			// filename carries the series, the filename now wins. The layout
			// tier only runs when the filename matches nothing at all.
			name:       "folder authoritative, filename carries the series",
			rel:        "Robert Jordan/The Eye of the World/The Wheel of Time 01.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "The Wheel of Time 01",
			wantLayout: "The Eye of the World",
		},
		{
			name:       "readarr series folder still strips its series prefix",
			rel:        "Terry Pratchett/Discworld/Discworld #8 - Guards! Guards!/x.epub",
			format:     models.MediaTypeEbook,
			wantTitle:  "x",
			wantLayout: "Guards! Guards!",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, layout := effectiveScanTitle(filepath.Join(root, filepath.FromSlash(c.rel)), root, c.format)
			if got != c.wantTitle {
				t.Errorf("title = %q, want %q", got, c.wantTitle)
			}
			if layout != c.wantLayout {
				t.Errorf("layout title = %q, want %q", layout, c.wantLayout)
			}
		})
	}
}

// TestStripLeadingPosition pins the prefix rule on its own, including the
// shapes it must leave alone.
func TestStripLeadingPosition(t *testing.T) {
	cases := []struct{ in, want, wantNum string }{
		{"01 - The Eye of the World", "The Eye of the World", "01"},
		{"01. Make Me Yours", "Make Me Yours", "01"},
		{"1 - Dune", "Dune", "1"},
		{"1.5 - Interlude", "Interlude", "1.5"},
		{"1984 - George Orwell", "1984 - George Orwell", ""},
		{"0001 - Project Hail Mary", "Project Hail Mary", "0001"},
		{"100 - Bullets", "Bullets", "100"},
		{"11-22-63 - Stephen King", "11-22-63 - Stephen King", ""},
		{"01", "01", ""},
		{"The Eye of the World", "The Eye of the World", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, num := stripLeadingPosition(c.in)
			if got != c.want || num != c.wantNum {
				t.Errorf("stripLeadingPosition(%q) = (%q, %q), want (%q, %q)", c.in, got, num, c.want, c.wantNum)
			}
		})
	}
}

// TestScanLibrary_LayoutTitleIsOnlyAFallback is the #2171 scan level
// regression. Two Discord reports, two layouts: a numbered file in a box set
// or series folder, and a "{series}/{seriesIndex} - {title}" book folder. In
// both the file used to be titled from the folder, so it reconciled onto the
// series opener (or onto nothing) and the real book stayed Wanted.
//
// Each case seeds the decoy book the folder names alongside the real one, so
// the test says which book the file lands on rather than only that it landed.
func TestScanLibrary_LayoutTitleIsOnlyAFallback(t *testing.T) {
	cases := []struct {
		name   string
		author string
		titles []string
		// files are library-relative; the first is the one asserted on.
		files []string
		// want is the title of the book the first file must land on, or ""
		// for no book at all.
		want string
	}{
		{
			name:   "numbered file in a series folder",
			author: "Robert Jordan",
			titles: []string{"The Eye of the World", "The Wheel of Time"},
			files:  []string{"Robert Jordan/The Wheel of Time/01 - The Eye of the World (1990).epub"},
			want:   "The Eye of the World",
		},
		{
			name:   "dotted position prefix",
			author: "Kandi Steiner",
			titles: []string{"Make Me Yours", "The Player"},
			files:  []string{"Kandi Steiner/The Player/01. Make Me Yours.epub"},
			want:   "Make Me Yours",
		},
		{
			name:   "series index in the book folder",
			author: "Robert Jordan",
			titles: []string{"The Eye of the World", "The Wheel of Time"},
			files:  []string{"Robert Jordan/The Wheel of Time/01 - The Eye of the World (1990)/The Eye of the World (1990).epub"},
			want:   "The Eye of the World",
		},
		{
			name:   "book folder names the book and the file does not",
			author: "Robert Jordan",
			titles: []string{"The Great Hunt", "The Wheel of Time"},
			files:  []string{"Robert Jordan/The Wheel of Time/02 - The Great Hunt (1990)/ebook.epub"},
			want:   "The Great Hunt",
		},
		{
			name:   "box set file naming its authors after the title",
			author: "Devney Perry, Amo Jones, Chelle Bliss",
			titles: []string{"Make Me Yours", "The Player"},
			files:  []string{"Devney Perry, Amo Jones, Chelle Bliss/The Player/01. Make Me Yours - Devney Perry, Amo Jones, Chelle Bliss (2020).epub"},
			want:   "Make Me Yours",
		},
		{
			name:   "the folder IS the title",
			author: "Andy Weir",
			titles: []string{"Project Hail Mary"},
			files:  []string{"Andy Weir/Project Hail Mary/Project Hail Mary.epub"},
			want:   "Project Hail Mary",
		},
		{
			name:   "flat author folder",
			author: "Andy Weir",
			titles: []string{"Project Hail Mary"},
			files:  []string{"Andy Weir/Project Hail Mary.epub"},
			want:   "Project Hail Mary",
		},
		{
			// The folder is the book here, and every file in it is a chapter.
			// Believing the filenames would give each track a different book.
			name:   "audiobook folder of per track files",
			author: "Andy Weir",
			titles: []string{"Project Hail Mary", "Sinister Grey Mists"},
			files: []string{
				"Andy Weir/Project Hail Mary/01 - Rocky.mp3",
				"Andy Weir/Project Hail Mary/02 - Sinister Grey Mists.mp3",
			},
			want: "Project Hail Mary",
		},
		{
			// The flat audiobook layout, with a book in the catalogue whose
			// title is track 01's chapter name. Stripping the position off an
			// audiobook filename handed this file to "Rocky" with confidence;
			// unmatched is what main does and what this must keep doing, so
			// the tracks reach library adoption as one unit instead.
			name:   "flat audiobook tracks with a book named after a chapter",
			author: "Andy Weir",
			titles: []string{"Project Hail Mary", "Rocky"},
			files: []string{
				"Andy Weir/01 - Rocky.mp3",
				"Andy Weir/02 - Sinister Grey Mists.mp3",
			},
			want: "",
		},
		{
			name:   "flat audiobook tracks with no book named after a chapter",
			author: "Andy Weir",
			titles: []string{"Project Hail Mary"},
			files: []string{
				"Andy Weir/01 - Rocky.mp3",
				"Andy Weir/02 - Sinister Grey Mists.mp3",
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			s, books, authors, ctx := scannerFixture(t, libDir)
			seeded := seedAuthorBooks(t, books, authors, ctx, c.author, c.titles...)

			var first string
			for i, rel := range c.files {
				p := filepath.Join(libDir, filepath.FromSlash(rel))
				writeFileAt(t, p)
				if i == 0 {
					first = p
				}
			}

			s.ScanLibrary(ctx)

			got := fileOwners(t, books, ctx, seeded, first)
			want := []string{c.want}
			if c.want == "" {
				want = nil
			}
			if !slices.Equal(got, want) {
				t.Errorf("file went to %v, want %v", got, want)
			}
		})
	}
}

// TestScanLibrary_TrackedFileIsNeverReattached is the #2171 upgrade guard: the
// title a file matches on changes, so an existing library rescans with
// different answers. A file that is already registered must not move. The scan
// skips every path in book_files before it parses anything, and book_files.path
// is UNIQUE with an INSERT OR IGNORE behind AddBookFile, so even a scan that
// got past the skip could not register the same path against a second book.
func TestScanLibrary_TrackedFileIsNeverReattached(t *testing.T) {
	libDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libDir)
	seeded := seedAuthorBooks(t, books, authors, ctx, "Robert Jordan", "The Eye of the World", "The Wheel of Time")

	p := filepath.Join(libDir, "Robert Jordan", "The Wheel of Time", "01 - The Eye of the World (1990).epub")
	writeFileAt(t, p)

	// The library as an older Bindery left it: the file is attached to the
	// book the folder named.
	decoy := seeded["The Wheel of Time"]
	if err := books.AddBookFile(ctx, decoy.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	if got := fileOwners(t, books, ctx, seeded, p); !slices.Equal(got, []string{"The Wheel of Time"}) {
		t.Errorf("tracked file moved: owners = %v, want [\"The Wheel of Time\"]", got)
	}
}
