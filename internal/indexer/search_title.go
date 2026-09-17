package indexer

import (
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// SearchTitle returns the title an indexer search for book should use.
//
// Some metadata stores a translated book under a bilingual display title,
// "El imperio final / The Final Empire". Sent to an indexer as it stands,
// that string matches no release, because a release is named in one language,
// and the same string then fails the title identity check on whatever did
// come back. For such a title this returns the localized half, which is what
// a release in the reader's language is called. Every other title comes back
// unchanged.
//
// The split is deliberately narrow, because a slash also joins the parts of a
// bundle ("Title A / Title B") that must be searched whole:
//
//   - exactly one " / ", with spaces, and not inside brackets or parentheses;
//   - both halves carry a letter or a digit;
//   - and there is evidence the pair is a translation: book.OriginalTitle
//     equals one half, or the book's language, or failing that the profile's
//     allowed languages, names a language other than English.
//
// OriginalTitle also orients the pair: the half it equals is the original, so
// the other half is returned even when a provider stored them in reverse.
//
// Adapted from IfritDMC's title candidate builder in #2391, reduced to the
// automatic search title.
func SearchTitle(book models.Book, allowedLanguages []string) string {
	if localized, ok := splitBilingualTitle(book.Title, book.OriginalTitle, book.Language, allowedLanguages); ok {
		return localized
	}
	return book.Title
}

func splitBilingualTitle(title, originalTitle, bookLanguage string, allowedLanguages []string) (string, bool) {
	const sep = " / "
	if strings.Count(title, sep) != 1 || separatorInsideGrouping(title, sep) {
		return "", false
	}
	parts := strings.SplitN(title, sep, 2)
	// The cleaned halves are only for validating and comparing. The half that
	// is returned keeps its own spelling, trimmed, so the search and the title
	// identity check see what they would for any other title.
	rawLeft, rawRight := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	left, right := cleanTitleHalf(rawLeft), cleanTitleHalf(rawRight)
	if left == "" || right == "" {
		return "", false
	}

	original := cleanTitleHalf(originalTitle)
	originalIsLeft := original != "" && strings.EqualFold(original, left)
	originalIsRight := original != "" && strings.EqualFold(original, right)
	if !originalIsLeft && !originalIsRight && !namesNonEnglish(bookLanguage, allowedLanguages) {
		return "", false
	}
	if originalIsLeft && !originalIsRight {
		return rawRight, true
	}
	return rawLeft, true
}

// namesNonEnglish reports whether the book's language, or when that is unset
// any of the profile's allowed languages, is a specific language other than
// English.
func namesNonEnglish(bookLanguage string, allowedLanguages []string) bool {
	specific := func(code string) bool {
		code = models.NormalizeLanguageCode(code)
		return code != "" && code != "eng" && code != "any"
	}
	if models.NormalizeLanguageCode(bookLanguage) != "" {
		return specific(bookLanguage)
	}
	for _, code := range allowedLanguages {
		if specific(code) {
			return true
		}
	}
	return false
}

// separatorInsideGrouping reports whether the first sep in title sits inside
// an open bracket or parenthesis, as in "Dune (Book 1 / Part 2)".
func separatorInsideGrouping(title, sep string) bool {
	idx := strings.Index(title, sep)
	if idx < 0 {
		return false
	}
	var round, square int
	for _, r := range title[:idx] {
		switch r {
		case '(':
			round++
		case ')':
			if round > 0 {
				round--
			}
		case '[':
			square++
		case ']':
			if square > 0 {
				square--
			}
		}
	}
	return round > 0 || square > 0
}

// cleanTitleHalf normalizes one half the way a query title is, and rejects a
// half with no letter or digit in it.
func cleanTitleHalf(title string) string {
	title = newznab.NormalizeQueryTitle(title)
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return title
		}
	}
	return ""
}
