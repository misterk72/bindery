package importer

import (
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Which name a library scan believes when the filename and the folder it sits
// in disagree (#2171).
//
// The scan used to let the folder win outright: a file under
// <root>/<Author>/<Book>/<file> took its title from the immediate parent
// folder, whatever the filename said. That is right for the layout it was
// written for (Author/Book Title/Book Title.epub) and wrong for every library
// that puts a box set or a series in that slot, because
// "Author/The Wheel of Time/01 - The Eye of the World (1990).epub" is then
// titled "The Wheel of Time". The file reconciles onto the series opener, or
// onto nothing, and the book it really is stays Wanted.
//
// Bulk import already resolved this the other way round: lookupUnit computes
// its effective title as firstNonEmpty(embedded, filename, layout), so the
// folder is only consulted when the filename yields nothing. The scan now uses
// the same rule and the same helper for ebooks.

// stripLeadingPosition removes a "01 - " or "1. " position prefix from a name
// derived from a file or folder, and returns the number it removed ("" when
// there was nothing to remove). leadingNumRe already recognises the prefix;
// ParseFilename only applies it once a series is known, which leaves the
// common "Series/01 - Title.epub" shape parsing as title "01".
//
// The digit-count guard keeps a real title that opens with a year intact:
// "1984 - George Orwell" matches the same regex, and positions of four digits
// do not happen in practice. A prefix with nothing after it is left alone too,
// so a file named "01.epub" keeps whatever little it has.
func stripLeadingPosition(name string) (rest, number string) {
	m := leadingNumRe.FindStringSubmatch(name)
	if len(m) != 2 {
		return name, ""
	}
	if whole, _, _ := strings.Cut(m[1], "."); len(whole) > 3 {
		return name, ""
	}
	stripped := strings.TrimSpace(leadingNumRe.ReplaceAllString(name, ""))
	if stripped == "" {
		return name, ""
	}
	return stripped, m[1]
}

// parseScanFile is ParseFilename for the library scan, with a leading position
// number removed first.
//
// ParseFilename strips that prefix only when it has already recognised a
// series, and a plain "The Wheel of Time" folder carries no series marker for
// it to recognise. So "01 - The Eye of the World (1990).epub" reaches the
// generic "Title - Author" split as "01 - The Eye of the World" and parses
// with title "01" and author "The Eye of the World". Removing the prefix
// before parsing gives the title the file actually names, and hands the number
// to the series tier when a series is known but unnumbered.
//
// The unstripped parse is kept as the fallback: if removing the prefix leaves
// nothing that parses as a title, the file is parsed as it always was.
func parseScanFile(path string) ParsedFile {
	parsed := ParseFilename(path)
	ext := filepath.Ext(path)
	base := dashNormalizer.Replace(strings.TrimSuffix(filepath.Base(path), ext))
	stripped, number := stripLeadingPosition(base)
	if number == "" {
		return parsed
	}
	alt := ParseFilename(filepath.Join(filepath.Dir(path), stripped+ext))
	if alt.Title == "" {
		return parsed
	}
	// Everything the untouched name carried that the stripped one lost.
	alt.FilePath = parsed.FilePath
	if alt.ASIN == "" {
		alt.ASIN = parsed.ASIN
	}
	if alt.ISBN == "" {
		alt.ISBN = parsed.ISBN
	}
	if alt.Year == "" {
		alt.Year = parsed.Year
	}
	if alt.Series == "" {
		alt.Series, alt.SeriesNumber = parsed.Series, parsed.SeriesNumber
	}
	if alt.Series != "" && alt.SeriesNumber == "" {
		alt.SeriesNumber = number
	}
	return alt
}

// scanTitle picks the title the library scan matches a file on, given what the
// filename parsed to and what the folder layout says.
//
// Ebooks follow bulk import: one ebook file is one book, so its own name is
// the better signal and the folder is the fallback (#2171).
//
// Audiobooks keep the folder first. A multi track audiobook is one book spread
// over files whose names are chapter or track names ("04 - Sinister Grey
// Mists.mp3"), so believing the filename there would give every track a
// different book and reconcile none of them (#1239), and would leave library
// adoption naming the unit after one track (representative, unmatched_units).
// The folder is the book for that layout, which is why this stays as it was.
func scanTitle(parsedTitle, layoutTitle, detectedFormat string) string {
	if detectedFormat == models.MediaTypeAudiobook {
		return firstNonEmpty(layoutTitle, parsedTitle)
	}
	return firstNonEmpty(parsedTitle, layoutTitle)
}
