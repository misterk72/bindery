package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const isbnGuardLookupISBN = "9783844935776"

// isbnGuardDescription is long enough to skip the aggregator's enrichment
// pass, which would otherwise call back into the stubs.
const isbnGuardDescription = "A description long enough that the ISBN lookup does not try to enrich it."

func lookupISBNForTest(t *testing.T, h *SearchHandler) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?isbn="+isbnGuardLookupISBN, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// TestLookupISBN_RefusesFallbackWhilePrimaryDownAndAsksPrimaryAgain: the Add
// Book dialog's ISBN search took DNB's record while OpenLibrary timed out, and
// adding it bound the book and its author to DNB for good (#2612). The lookup
// now refuses with the primary named, and the refused answer is not cached,
// so the next lookup, once the primary is back, returns the primary's record.
func TestLookupISBN_RefusesFallbackWhilePrimaryDownAndAsksPrimaryAgain(t *testing.T) {
	primary := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		isbnErr:          errors.New("openlibrary: context deadline exceeded"),
	}
	dnb := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "dnb"},
		isbnBook: &models.Book{
			ForeignID:   "dnb:1305873874",
			Title:       "Der war's",
			Description: isbnGuardDescription,
			Author:      &models.Author{ForeignID: "dnb:gnd:123120802", Name: "Juli Zeh"},
		},
	}
	h := NewSearchHandler(metadata.NewAggregator(primary, dnb), nil, nil)

	rec, body := lookupISBNForTest(t, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("primary down: status = %d, want 503, body %s", rec.Code, rec.Body.String())
	}
	if _, ok := body["foreignBookId"]; ok {
		t.Errorf("a refused lookup must return no record, got %s", rec.Body.String())
	}
	msg, _ := body["error"].(string)
	for _, want := range []string{"openlibrary", "did not answer", "try again"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
		}
	}

	primary.isbnErr = nil
	primary.isbnBook = &models.Book{
		ForeignID:   "OL45804W",
		Title:       "Der war's",
		Description: isbnGuardDescription,
		Author:      &models.Author{ForeignID: "OL1A", Name: "Juli Zeh"},
	}
	rec, body = lookupISBNForTest(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("primary back: status = %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	if got := body["foreignBookId"]; got != "OL45804W" {
		t.Errorf("primary back: foreignBookId = %v, want the primary's OL45804W rather than a cached fallback", got)
	}
}

// TestLookupISBN_KeepsFallbackWhenPrimaryMerelyMisses is the #2237 rule: a
// primary that answers without the ISBN is a fact, so the fallback's record is
// the right answer.
func TestLookupISBN_KeepsFallbackWhenPrimaryMerelyMisses(t *testing.T) {
	primary := &isbnMetaProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}}
	dnb := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "dnb"},
		isbnBook: &models.Book{
			ForeignID:   "dnb:1305873874",
			Title:       "Der war's",
			Description: isbnGuardDescription,
			Author:      &models.Author{ForeignID: "dnb:gnd:123120802", Name: "Juli Zeh"},
		},
	}
	h := NewSearchHandler(metadata.NewAggregator(primary, dnb), nil, nil)

	rec, body := lookupISBNForTest(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	if got := body["foreignBookId"]; got != "dnb:1305873874" {
		t.Errorf("foreignBookId = %v, want the fallback's dnb:1305873874", got)
	}
}
