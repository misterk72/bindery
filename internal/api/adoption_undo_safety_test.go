package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// These tests come from the backend review of library adoption. Each one is a
// way Undo or adopt could lose data that was not the adoption's to lose.

func (f adoptionFixture) adoptWells(t *testing.T, id int64) adoptionItem {
	t.Helper()
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{
		"foreignBookId": "OL27482W", "foreignAuthorId": "OL39307A", "authorName": "H. G. Wells",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	it := decodeItem(t, rec)
	if !it.BookCreated || !it.AuthorCreated || it.Book == nil {
		t.Fatalf("adopt did not create book and author: %+v", it)
	}
	return it
}

// TestUndo_LeavesAFileThatNowBelongsToAnotherBook: adopt into B, delete B,
// register the same path to C, Undo. C keeps its file.
func TestUndo_LeavesAFileThatNowBelongsToAnotherBook(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	ctx := context.Background()
	b := f.seedBook(t, "Ancillary Justice")
	c := f.seedBook(t, "Ancillary Sword")
	path := f.write(t, "Ann Leckie/Ancillary.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": b.ID}); rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	if err := f.books.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, c.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, c.ID); len(got) != 1 || got[0] != path {
		t.Fatalf("book C files after undo = %v, want it to keep %s", got, path)
	}
	if u, _ := f.units.Get(ctx, id); u.State != db.UnmatchedStatePending {
		t.Fatalf("unit state = %s, want pending", u.State)
	}
}

// TestUndo_KeepsACreatedAuthorWithAnExcludedBook: the created author gains an
// excluded book with a file. Undo must not delete the author, which would
// cascade to that book.
func TestUndo_KeepsACreatedAuthorWithAnExcludedBook(t *testing.T) {
	f := newAdoptionFixture(t, addBookBackCatalogueStub(false))
	ctx := context.Background()
	path := f.write(t, "H. G. Wells/War of the Worlds.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
	it := f.adoptWells(t, id)
	adopted, _ := f.books.GetByID(ctx, it.Book.ID)

	other := &models.Book{ForeignID: "OL-excluded", AuthorID: adopted.AuthorID, Title: "The Time Machine",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherPath := f.write(t, "H. G. Wells/The Time Machine.epub")
	if err := f.books.AddBookFile(ctx, other.ID, models.MediaTypeEbook, otherPath); err != nil {
		t.Fatal(err)
	}
	if err := f.books.SetExcluded(ctx, other.ID, true); err != nil {
		t.Fatal(err)
	}

	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil); rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := f.authors.GetByID(ctx, adopted.AuthorID); a == nil {
		t.Fatal("created author deleted although it still has an excluded book")
	}
	if b, _ := f.books.GetByID(ctx, other.ID); b == nil {
		t.Fatal("excluded book deleted with the author")
	}
	if got := filePaths(t, f.books, other.ID); len(got) != 1 {
		t.Fatalf("excluded book files = %v, want its file kept", got)
	}
}

// TestUndo_KeepsACreatedBookSomeoneStartedUsing: after the adoption the book
// was monitored, or linked into a series. Undo untracks the adopted files and
// returns the unit to pending, but keeps the book and author, and says so.
func TestUndo_KeepsACreatedBookSomeoneStartedUsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		use  func(t *testing.T, f adoptionFixture, book *models.Book)
	}{
		{"monitored", func(t *testing.T, f adoptionFixture, book *models.Book) {
			book.Monitored = true
			if err := f.books.Update(context.Background(), book); err != nil {
				t.Fatal(err)
			}
		}},
		{"blocklist entry", func(t *testing.T, f adoptionFixture, book *models.Book) {
			if _, err := f.db.Exec(`INSERT INTO blocklist (book_id, guid, title) VALUES (?, 'guid-1', 'War of the Worlds retail')`, book.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"calibre id", func(t *testing.T, f adoptionFixture, book *models.Book) {
			// Written the way the Calibre integration writes it: the column
			// alone, without touching updated_at.
			if _, err := f.db.Exec(`UPDATE books SET calibre_id = 42 WHERE id = ?`, book.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"series link", func(t *testing.T, f adoptionFixture, book *models.Book) {
			series := db.NewSeriesRepo(f.db)
			s := &models.Series{ForeignID: "OL-series", Title: "Scientific Romances"}
			if err := series.CreateOrGet(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if err := series.LinkBook(context.Background(), s.ID, book.ID, "1", true); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdoptionFixture(t, addBookBackCatalogueStub(false))
			ctx := context.Background()
			path := f.write(t, "H. G. Wells/War of the Worlds.epub")
			id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
			it := f.adoptWells(t, id)
			book, _ := f.books.GetByID(ctx, it.Book.ID)
			tc.use(t, f, book)

			rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if kept, _ := f.books.GetByID(ctx, book.ID); kept == nil {
				t.Fatal("created book deleted although it was in use")
			}
			if a, _ := f.authors.GetByID(ctx, book.AuthorID); a == nil {
				t.Fatal("created author deleted although its book was kept")
			}
			if got := filePaths(t, f.books, book.ID); len(got) != 0 {
				t.Fatalf("adopted files still tracked: %v", got)
			}
			if msg, _ := body["message"].(string); msg == "" {
				t.Errorf("undo response has no message saying the book was kept: %s", rec.Body.String())
			}
		})
	}
}

// TestAdopt_RejectsAFormatThatContradictsTheFiles: audio files cannot be
// adopted as an ebook, nor ebooks as an audiobook.
func TestAdopt_RejectsAFormatThatContradictsTheFiles(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Provenance")
	epub := f.write(t, "Ann Leckie/Provenance.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub}})
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID, "format": "audiobook"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ebook adopted as audiobook = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
}

// TestAdopt_RegistersEachDiscFolder: a disc set is registered disc folder by
// disc folder, the shape the scan already counts as tracked, so no scanner
// rule about disc folders under a tracked folder is needed.
func TestAdopt_RegistersEachDiscFolder(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Artemis")
	cd1 := f.write(t, "Andy Weir/Artemis/CD1/01.mp3")
	cd2 := f.write(t, "Andy Weir/Artemis/CD2/01.mp3")
	folder := filepath.Dir(filepath.Dir(cd1))
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: folder, UnitKind: db.UnmatchedKindFolder,
		Format: models.MediaTypeAudiobook, MemberPaths: []string{cd1, cd2}})
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID}); rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	got := filePaths(t, f.books, book.ID)
	if len(got) != 2 || got[0] != filepath.Dir(cd1) || got[1] != filepath.Dir(cd2) {
		t.Fatalf("registered %v, want the two disc folders", got)
	}
}

// TestAdoptionBodies_RejectUnknownFields: no adoption route takes a path, and
// one sent anyway is refused with 400 instead of being silently ignored.
func TestAdoptionBodies_RejectUnknownFields(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Provenance")
	epub := f.write(t, "Ann Leckie/Provenance.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub}, AuthorFolder: "Ann Leckie"})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID, "path": "/etc/passwd"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "path") {
		t.Fatalf("adopt with a path field = %d %s, want 400 naming the field", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
	rec = f.post(t, "/library/unmatched/ignore", map[string]any{"authorFolder": "Ann Leckie", "path": "/etc"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bulk ignore with a path field = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if u, _ := f.units.Get(context.Background(), id); u.State != db.UnmatchedStatePending {
		t.Fatalf("unit state = %s, want still pending", u.State)
	}
}
