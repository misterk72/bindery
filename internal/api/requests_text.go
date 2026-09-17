package api

import (
	"strings"
	"unicode"
)

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

// cleanRequestText strips control and invisible formatting characters
// (including bidi overrides and zero width characters, which can disguise
// text in a notification), collapses runs of whitespace, trims, and caps the
// result at maxRunes.
func cleanRequestText(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true
	n := 0
	for _, r := range s {
		if n >= maxRunes {
			break
		}
		switch {
		case r == '\uFFFD' || unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !unicode.IsSpace(r)):
			continue
		case unicode.IsSpace(r):
			if lastSpace {
				continue
			}
			b.WriteRune(' ')
			lastSpace = true
		default:
			b.WriteRune(r)
			lastSpace = false
		}
		n++
	}
	return strings.TrimSpace(b.String())
}

// neutraliseMentions replaces every at sign with the fullwidth at sign, which
// reads the same to a person and is not a mention to Discord, Slack or
// Matrix, so "@everyone" in a title or a username cannot ping a channel.
func neutraliseMentions(s string) string {
	return strings.ReplaceAll(s, "@", "\uFF20")
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
