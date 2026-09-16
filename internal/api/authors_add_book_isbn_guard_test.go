package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// isbnMetaProvider is stubMetaProvider with a scripted ISBN lookup, for the
// Add Book path that resolves an id-less result's author by ISBN (#2612).
type isbnMetaProvider struct {
	stubMetaProvider
	isbnBook *models.Book
	isbnErr  error
}

func (p *isbnMetaProvider) GetBookByISBN(_ context.Context, _ string) (*models.Book, error) {
	return p.isbnBook, p.isbnErr
}

const isbnGuardBookID = "dnb:1305873874"

// isbnGuardFixture wires an OpenLibrary primary and a DNB enricher. The DNB
// book carries an ISBN edition and no author id, which is exactly the shape
// that sends AddBook through resolveAuthorForBook.
type isbnGuardFixture struct {
	authors *db.AuthorRepo
	books   *db.BookRepo
	handler *AuthorHandler
}

func newISBNGuardFixture(t *testing.T, primary, dnb *isbnMetaProvider) *isbnGuardFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	isbn := "9783844935776"
	dnb.getBookByID = map[string]*models.Book{
		isbnGuardBookID: {
			ForeignID:        isbnGuardBookID,
			Title:            "Der war's",
			SortTitle:        "Der war's",
			Language:         "ger",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "dnb",
			Editions:         []models.Edition{{ISBN13: &isbn}},
		},
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	agg := metadata.NewAggregator(primary, dnb)
	return &isbnGuardFixture{
		authors: authorRepo,
		books:   bookRepo,
		handler: NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, db.NewMetadataProfileRepo(database), nil),
	}
}

func (f *isbnGuardFixture) addBook(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"foreignBookId": isbnGuardBookID,
		"authorName":    "Juli Zeh",
	})
	rec := httptest.NewRecorder()
	f.handler.AddBook(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)))
	return rec
}

func (f *isbnGuardFixture) assertNothingCreated(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	authors, err := f.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	books, err := f.books.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 0 || len(books) != 0 {
		t.Errorf("a refused add must create nothing, got %d authors and %d books", len(authors), len(books))
	}
}

func assertPrimaryUnavailableBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	msg := body["error"]
	if !strings.Contains(msg, "did not answer") || !strings.Contains(msg, "try again") {
		t.Errorf("error = %q, want it to say the primary did not answer and to try again", msg)
	}
}

// TestAddBook_ISBNResolveRefusesFallbackWhilePrimaryDown: with the primary
// timing out, the ISBN walk moved on to DNB and AddBook rewrote the request to
// DNB's ids, making DNB the author's permanent provider on the strength of a
// few seconds of outage (#2612, the interactive half of #2332).
func TestAddBook_ISBNResolveRefusesFallbackWhilePrimaryDown(t *testing.T) {
	primary := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		isbnErr:          errors.New("openlibrary: context deadline exceeded"),
	}
	dnb := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "dnb"},
		isbnBook: &models.Book{
			ForeignID: isbnGuardBookID,
			Title:     "Der war's",
			Author:    &models.Author{ForeignID: "dnb:gnd:123120802", Name: "Juli Zeh"},
		},
	}
	f := newISBNGuardFixture(t, primary, dnb)

	rec := f.addBook(t)
	assertPrimaryUnavailableBody(t, rec)
	f.assertNothingCreated(t)
}

// TestAddBook_ISBNResolveRefusesNameFallbackWhilePrimaryDown: when no provider
// places the author by ISBN and the primary was one of the ones that failed,
// the "add the author manually" hint is the wrong advice. The primary may well
// know this book, so the answer is to try again.
func TestAddBook_ISBNResolveRefusesNameFallbackWhilePrimaryDown(t *testing.T) {
	primary := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		isbnErr:          errors.New("openlibrary: context deadline exceeded"),
	}
	dnb := &isbnMetaProvider{stubMetaProvider: stubMetaProvider{name: "dnb"}}
	f := newISBNGuardFixture(t, primary, dnb)

	rec := f.addBook(t)
	assertPrimaryUnavailableBody(t, rec)
	f.assertNothingCreated(t)
}

// TestAddBook_ISBNResolveKeepsFallbackWhenPrimaryMerelyMisses is the #2237
// counterweight: a primary that answers and has no record for the ISBN is a
// fact, and the fallback's record is the right one to add.
func TestAddBook_ISBNResolveKeepsFallbackWhenPrimaryMerelyMisses(t *testing.T) {
	primary := &isbnMetaProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}}
	dnb := &isbnMetaProvider{
		stubMetaProvider: stubMetaProvider{name: "dnb"},
		isbnBook: &models.Book{
			ForeignID: isbnGuardBookID,
			Title:     "Der war's",
			Author:    &models.Author{ForeignID: "dnb:gnd:123120802", Name: "Juli Zeh"},
		},
	}
	f := newISBNGuardFixture(t, primary, dnb)

	rec := f.addBook(t)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body %s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	author, err := f.authors.GetByForeignID(ctx, "dnb:gnd:123120802")
	if err != nil || author == nil {
		t.Fatalf("fallback author not created: err=%v author=%v", err, author)
	}
	book, err := f.books.GetByForeignID(ctx, isbnGuardBookID)
	if err != nil || book == nil {
		t.Fatalf("book not created: err=%v book=%v", err, book)
	}
	if book.AuthorID != author.ID {
		t.Errorf("book author = %d, want %d", book.AuthorID, author.ID)
	}
}
