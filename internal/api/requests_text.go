package api

import "github.com/vavallee/bindery/internal/notifier"

// Length caps, in runes, for text a request carries. Titles and names come
// from a metadata provider, and OpenLibrary is publicly editable, so they are
// capped and cleaned like any outside input.
const (
	requestTitleMaxRunes    = 300
	requestAuthorMaxRunes   = 200
	requestUsernameMaxRunes = 64
	requestReasonMaxRunes   = 500
	requestForeignIDMaxLen  = 128
)

// cleanRequestText is notifier.CleanText: control and invisible characters
// stripped, whitespace collapsed, capped. Stored text goes through it; text
// sent to a webhook goes through notifier.SafeText as well.
func cleanRequestText(s string, maxRunes int) string {
	return notifier.CleanText(s, maxRunes)
}

// validForeignID reports whether id is a plausible provider id: non empty,
// bounded, and printable ASCII without spaces. Every provider id Bindery
// issues (OL123W, hc:slug, dnb:123, gb:abc_d) fits.
func validForeignID(id string) bool {
	if id == "" || len(id) > requestForeignIDMaxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}
