package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// ListBookSeriesMembershipsByAuthor carries the series foreign id, position
// and primary flag alongside the ids, which is what a catalogue sync needs to
// decide whether a provider ref is already stored without a query per book
// (#2328). A manually created series comes back too, under the synthetic
// foreign id CreateManual gives it, so it counts towards the book already
// having a primary series while never matching a provider ref.
func TestListBookSeriesMembershipsByAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{ForeignID: "OL-AM", Name: "M", SortName: "M", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	other := &models.Author{ForeignID: "OL-AN", Name: "N", SortName: "N", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	mine := &models.Book{ForeignID: "OL-M1", AuthorID: author.ID, Title: "M1", SortTitle: "M1", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	loose := &models.Book{ForeignID: "OL-M2", AuthorID: author.ID, Title: "M2", SortTitle: "M2", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	theirs := &models.Book{ForeignID: "OL-N1", AuthorID: other.ID, Title: "N1", SortTitle: "N1", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	for _, b := range []*models.Book{mine, loose, theirs} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	provider := &models.Series{ForeignID: "ol-series:P", Title: "Provider"}
	if err := seriesRepo.CreateOrGet(ctx, provider); err != nil {
		t.Fatal(err)
	}
	manual, err := seriesRepo.CreateManual(ctx, "Handmade")
	if err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, provider.ID, mine.ID, "3", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, manual.ID, loose.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, provider.ID, theirs.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	got, err := seriesRepo.ListBookSeriesMembershipsByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatalf("list memberships: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d books with memberships, want the 2 under this author", len(got))
	}
	if _, ok := got[theirs.ID]; ok {
		t.Error("another author's book leaked into the result")
	}
	if len(got[mine.ID]) != 1 {
		t.Fatalf("mine: got %v, want one membership", got[mine.ID])
	}
	m := got[mine.ID][0]
	if m.SeriesID != provider.ID || m.SeriesForeignID != "ol-series:P" || m.Position != "3" || !m.Primary {
		t.Errorf("mine: got %+v, want the provider series at position 3, primary", m)
	}
	if len(got[loose.ID]) != 1 {
		t.Fatalf("loose: got %v, want one membership", got[loose.ID])
	}
	if l := got[loose.ID][0]; l.SeriesID != manual.ID || !l.Primary ||
		!strings.HasPrefix(l.SeriesForeignID, "manual:series:") {
		t.Errorf("loose: got %+v, want a primary membership in the manual series", l)
	}
}
