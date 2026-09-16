package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_KeepsExistingBookUnmonitored covers #2632: applyBookFields
// stamped Monitored = true on every existing book an import matched, so one
// run re-monitored a whole deliberately curated library. The user's choice
// wins on update; only a book the import creates gets the monitored default.
func TestImporter_KeepsExistingBookUnmonitored(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-KU", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{
		ForeignID:        "OL-KU-ARTEMIS",
		AuthorID:         author.ID,
		Title:            "Artemis",
		SortTitle:        "Artemis",
		Monitored:        false,
		Status:           models.BookStatusWanted,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-artemis"
	item.Title = "Artemis"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-weir", Name: "Andy Weir"}}
	runSingleABSImport(t, importer, item)

	got, err := bookRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("existing book vanished")
	}
	if got.Monitored {
		t.Errorf("existing unmonitored book was re-monitored by the import (#2632)")
	}
	if countBooksForAuthor(t, bookRepo, author.ID) != 1 {
		t.Fatalf("import should have matched the existing book, not created one")
	}

	// A second run hits the provenance and foreign id paths rather than the
	// title match; the choice must survive those too.
	runSingleABSImport(t, importer, item)
	got, err = bookRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Monitored {
		t.Errorf("re-import re-monitored the book (#2632)")
	}

	// A book the import creates still gets the monitored default.
	created := sampleABSItem()
	created.ItemID = "li-hail-mary"
	created.ASIN = ""
	created.Series = nil
	created.Authors = []NormalizedAuthor{{ID: "author-weir", Name: "Andy Weir"}}
	runSingleABSImport(t, importer, created)
	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, b := range books {
		if b.ID == existing.ID {
			continue
		}
		found = true
		if !b.Monitored {
			t.Errorf("newly created book %q should be monitored", b.Title)
		}
	}
	if !found {
		t.Fatal("expected the import to create a second book")
	}
}
