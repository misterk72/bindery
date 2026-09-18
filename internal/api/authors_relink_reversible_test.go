package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The reporter's case in #2688: Amy Tan, linked to the good OpenLibrary record
// OL220796A, relinked to the sparse duplicate OL15403031A, and then stranded
// there because the good record never came back in the picker.
const (
	relinkGoodAmyTanID   = "OL220796A"
	relinkSparseAmyTanID = "OL15403031A"
)

func newAmyTanRelinkFixture(t *testing.T) *relinkUpstreamFixture {
	t.Helper()

	return newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{
			"Amy Tan": {
				{ForeignID: relinkGoodAmyTanID, Name: "Amy Tan", MetadataProvider: "openlibrary"},
				{ForeignID: relinkSparseAmyTanID, Name: "Amy Tan", MetadataProvider: "openlibrary"},
			},
		},
		authors: map[string]*models.Author{
			relinkGoodAmyTanID: {
				ForeignID:        relinkGoodAmyTanID,
				Name:             "Amy Tan",
				SortName:         "Tan, Amy",
				Description:      "Author of The Joy Luck Club.",
				MetadataProvider: "openlibrary",
			},
			relinkSparseAmyTanID: {
				ForeignID:        relinkSparseAmyTanID,
				Name:             "Amy Tan",
				SortName:         "Tan, Amy",
				MetadataProvider: "openlibrary",
			},
		},
	})
}

func decodeRelinkCandidates(t *testing.T, body []byte) []relinkCandidate {
	t.Helper()

	var got []relinkCandidate
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode candidates: %v (%s)", err, body)
	}
	return got
}

func findRelinkCandidate(candidates []relinkCandidate, foreignID string) *relinkCandidate {
	for i := range candidates {
		if candidates[i].ForeignID == foreignID {
			return &candidates[i]
		}
	}
	return nil
}

// TestRelinkCandidates_OffersPreviouslyLinkedRecord is the direct regression
// test for #2688. After relinking away from a record, that record must come
// back in the picker, flagged rather than hidden. Before the fix it was
// filtered out because relinking had written it into author_identifiers.
func TestRelinkCandidates_OffersPreviouslyLinkedRecord(t *testing.T) {
	fixture := newAmyTanRelinkFixture(t)
	author := fixture.createAuthor(t, &models.Author{
		ForeignID:        relinkGoodAmyTanID,
		Name:             "Amy Tan",
		SortName:         "Tan, Amy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	if rec := fixture.relinkTo(t, author.ID, relinkSparseAmyTanID); rec.Code != http.StatusOK {
		t.Fatalf("relink to sparse record: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec := fixture.candidates(t, author.ID, "Amy%20Tan")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeRelinkCandidates(t, rec.Body.Bytes())

	good := findRelinkCandidate(got, relinkGoodAmyTanID)
	if good == nil {
		t.Fatalf("previously linked record %s missing from candidates: %+v", relinkGoodAmyTanID, got)
	}
	if !good.PreviouslyLinked {
		t.Fatalf("candidate %s previouslyLinked = false, want true", relinkGoodAmyTanID)
	}
	if current := findRelinkCandidate(got, relinkSparseAmyTanID); current != nil {
		t.Fatalf("current link %s must stay excluded, got %+v", relinkSparseAmyTanID, current)
	}
}

// TestRelinkCandidates_FlagIsAbsentOnUntouchedCandidates keeps the flag honest:
// a record the author was never linked to must not be marked, and the JSON must
// omit the key entirely so the picker renders nothing.
func TestRelinkCandidates_FlagIsAbsentOnUntouchedCandidates(t *testing.T) {
	fixture := newAmyTanRelinkFixture(t)
	author := fixture.createAuthor(t, &models.Author{
		ForeignID:        relinkSparseAmyTanID,
		Name:             "Amy Tan",
		SortName:         "Tan, Amy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.candidates(t, author.ID, "Amy%20Tan")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeRelinkCandidates(t, rec.Body.Bytes())
	good := findRelinkCandidate(got, relinkGoodAmyTanID)
	if good == nil {
		t.Fatalf("candidate %s missing: %+v", relinkGoodAmyTanID, got)
	}
	if good.PreviouslyLinked {
		t.Fatalf("candidate %s previouslyLinked = true, want false", relinkGoodAmyTanID)
	}

	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, row := range raw {
		if _, ok := row["previouslyLinked"]; ok {
			t.Fatalf("previouslyLinked must be omitted for untouched candidates: %+v", row)
		}
	}
}

// TestRelinkCandidates_KeepsOtherProviderIdentifiersSelectable: the rule is per
// foreign id, not per provider. An author carrying a Hardcover identifier
// alongside an OpenLibrary link can still pick the Hardcover record, flagged the
// same way as any other former link.
func TestRelinkCandidates_KeepsOtherProviderIdentifiersSelectable(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t,
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Amy Tan": {{ForeignID: relinkGoodAmyTanID, Name: "Amy Tan", MetadataProvider: "openlibrary"}},
			},
		},
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Amy Tan": {{ForeignID: "hc:amy-tan", Name: "Amy Tan", MetadataProvider: "hardcover"}},
			},
		},
	)
	author := fixture.createAuthor(t, &models.Author{
		ForeignID:        relinkGoodAmyTanID,
		Name:             "Amy Tan",
		SortName:         "Tan, Amy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})
	if err := fixture.authors.UpsertAuthorIdentifier(fixture.ctx, author.ID, "hc:amy-tan"); err != nil {
		t.Fatal(err)
	}

	rec := fixture.candidates(t, author.ID, "Amy%20Tan")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeRelinkCandidates(t, rec.Body.Bytes())

	hardcover := findRelinkCandidate(got, "hc:amy-tan")
	if hardcover == nil {
		t.Fatalf("hardcover identifier must stay selectable: %+v", got)
	}
	if !hardcover.PreviouslyLinked {
		t.Fatalf("hc:amy-tan previouslyLinked = false, want true")
	}
	if current := findRelinkCandidate(got, relinkGoodAmyTanID); current != nil {
		t.Fatalf("current OpenLibrary link must stay excluded, got %+v", current)
	}
}

// TestRelinkUpstream_RoundTripsBackToPreviousRecord walks the whole reported
// journey: good record, sparse duplicate, back to the good record. The author
// must end up on the record it started from, the identifier table must hold
// each id exactly once, and the books must not have multiplied.
func TestRelinkUpstream_RoundTripsBackToPreviousRecord(t *testing.T) {
	fixture := newAmyTanRelinkFixture(t)
	author := fixture.createAuthor(t, &models.Author{
		ForeignID:        relinkGoodAmyTanID,
		Name:             "Amy Tan",
		SortName:         "Tan, Amy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})
	book := &models.Book{
		ForeignID:        "OL1W",
		AuthorID:         author.ID,
		Title:            "The Joy Luck Club",
		SortTitle:        "joy luck club",
		Status:           "imported",
		Genres:           []string{},
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := fixture.books.Create(fixture.ctx, book); err != nil {
		t.Fatal(err)
	}

	if rec := fixture.relinkTo(t, author.ID, relinkSparseAmyTanID); rec.Code != http.StatusOK {
		t.Fatalf("relink to sparse record: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fixture.relinkTo(t, author.ID, relinkGoodAmyTanID); rec.Code != http.StatusOK {
		t.Fatalf("relink back: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := fixture.authors.GetByID(fixture.ctx, author.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != relinkGoodAmyTanID {
		t.Fatalf("author foreign id = %q, want %q", got.ForeignID, relinkGoodAmyTanID)
	}
	if got.MetadataProvider != "openlibrary" {
		t.Fatalf("author provider = %q, want openlibrary", got.MetadataProvider)
	}

	// Both ids stay recorded, once each: the one we came back to is now the
	// primary link, and the sparse one it came from stays so books minted
	// under it still resolve (#1705).
	identifiers, err := fixture.authors.ListAuthorIdentifiers(fixture.ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, identifier := range identifiers {
		seen[identifier.ForeignID]++
		if identifier.AuthorID != author.ID {
			t.Fatalf("identifier %q owned by author %d, want %d", identifier.ForeignID, identifier.AuthorID, author.ID)
		}
	}
	if len(identifiers) != 2 || seen[relinkGoodAmyTanID] != 1 || seen[relinkSparseAmyTanID] != 1 {
		t.Fatalf("identifier rows = %+v, want one row each for %s and %s", identifiers, relinkGoodAmyTanID, relinkSparseAmyTanID)
	}

	books, err := fixture.books.ListByAuthor(fixture.ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != book.ID || books[0].ForeignID != "OL1W" {
		t.Fatalf("books after round trip = %+v, want the single original row", books)
	}
}

// TestBuildRelinkCandidates_MatchesForeignIDsCaseInsensitively pins the
// comparison rule the handler used before this change, so loosening the filter
// did not quietly loosen the matching too.
func TestBuildRelinkCandidates_MatchesForeignIDsCaseInsensitively(t *testing.T) {
	candidates := []models.Author{
		{ForeignID: "ol220796a", Name: "Amy Tan"},
		{ForeignID: "OL15403031A", Name: "Amy Tan"},
		{ForeignID: "", Name: "Nameless record"},
	}
	got := buildRelinkCandidates(candidates, " OL220796A ", []models.AuthorIdentifier{
		{ForeignID: "ol15403031a"},
		{ForeignID: "OL220796A"},
	})

	if len(got) != 2 {
		t.Fatalf("candidates = %d, want 2: %+v", len(got), got)
	}
	if got[0].ForeignID != "OL15403031A" || !got[0].PreviouslyLinked {
		t.Fatalf("first candidate = %+v, want the flagged former link", got[0])
	}
	if got[1].ForeignID != "" || got[1].PreviouslyLinked {
		t.Fatalf("second candidate = %+v, want the unflagged id-less record", got[1])
	}
}
