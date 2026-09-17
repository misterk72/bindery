package notifier

import (
	"strings"
	"unicode"
)

// CleanText strips control and invisible formatting characters (including
// bidi overrides and zero width characters, which can disguise text),
// collapses runs of whitespace, trims, and caps the result at maxRunes. It
// changes nothing a person reads. Use it for text Bindery stores or shows.
func CleanText(s string, maxRunes int) string {
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

// chatMarkup maps each character chat services read as markup to a fullwidth
// lookalike that reads the same to a person and means nothing to the service:
//
//   - "@" mentions (@everyone, @here, @user on Discord, Matrix, Mattermost);
//   - "<" and ">" Slack and Discord escapes (<!channel>, <!here>, <@U123>,
//     <#C123>, <https://link|label>);
//   - "[" and "]" markdown links ([text](https://evil.example)), which cannot
//     form without the brackets.
var chatMarkup = strings.NewReplacer(
	"@", "\uFF20",
	"<", "\uFF1C",
	">", "\uFF1E",
	"[", "\uFF3B",
	"]", "\uFF3D",
)

// SafeText is CleanText plus chat markup neutralised (see chatMarkup), for
// text from outside Bindery that goes into a webhook payload: titles from an
// editable metadata provider, a self chosen username. requestCreated uses it,
// and any other event carrying such text should too.
func SafeText(s string, maxRunes int) string {
	return chatMarkup.Replace(CleanText(s, maxRunes))
}
