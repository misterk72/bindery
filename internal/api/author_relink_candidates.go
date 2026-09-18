package api

import (
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// relinkCandidate is one row of the GET /author/{id}/relink-upstream/candidates
// response. It is models.Author verbatim plus a hint about the author's own
// history, so the existing picker keeps working on the embedded fields and
// only has to opt in to the new one.
type relinkCandidate struct {
	models.Author
	// PreviouslyLinked marks a candidate the author used to be linked to and
	// is no longer. The picker renders it as a quiet marker; it is a label,
	// never a reason to hide the row.
	PreviouslyLinked bool `json:"previouslyLinked,omitempty"`
}

// buildRelinkCandidates turns raw provider search results into the relink
// picker's response.
//
// Only the author's CURRENT foreign id is dropped: relinking to the record you
// are already on is a no op. Every other candidate is returned, including ones
// that appear in author_identifiers because the author was linked to them
// before.
//
// Those historical rows used to be filtered out as well, which made relinking a
// one way door (#2688): relinkExistingAuthorToUpstream writes the old foreign id
// into author_identifiers on every relink so books minted under it still
// resolve (#1705), and that same row then hid the record from the picker
// forever. A user who moved from a good provider record to a sparse duplicate
// had no way back except adding the good record as a second author, which is
// the duplicate the identifier table exists to prevent.
//
// The rule is per foreign id, not per provider. An author can legitimately hold
// identifiers for OpenLibrary, Hardcover and others at once; each is selectable
// unless it is the one currently in authors.foreign_id, whichever provider it
// came from.
func buildRelinkCandidates(candidates []models.Author, currentForeignID string, identifiers []models.AuthorIdentifier) []relinkCandidate {
	current := strings.ToLower(strings.TrimSpace(currentForeignID))

	historical := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		key := strings.ToLower(strings.TrimSpace(identifier.ForeignID))
		if key == "" || key == current {
			// The current link is also an author_identifiers row (AuthorRepo
			// .Update upserts it), so it is not "previously" anything.
			continue
		}
		historical[key] = struct{}{}
	}

	out := make([]relinkCandidate, 0, len(candidates))
	for i := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidates[i].ForeignID))
		if key != "" && key == current {
			continue
		}
		proxyAuthorImages(&candidates[i])
		cleanAuthorDescription(&candidates[i])
		row := relinkCandidate{Author: candidates[i]}
		if key != "" {
			_, row.PreviouslyLinked = historical[key]
		}
		out = append(out, row)
	}
	return out
}
