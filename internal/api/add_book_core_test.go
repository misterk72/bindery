package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// worksCountingProvider counts the author works calls a catalogue sync makes,
// so a test can tell whether addBookCore or createAuthorCore started one.
type worksCountingProvider struct {
	*stubMetaProvider
	worksCalls atomic.Int32
}

func (p *worksCountingProvider) GetAuthorWorks(ctx context.Context, id string) ([]models.Book, error) {
	p.worksCalls.Add(1)
	return p.stubMetaProvider.GetAuthorWorks(ctx, id)
}

func (p *worksCountingProvider) GetAuthorWorksByName(ctx context.Context, name string) ([]models.Book, error) {
	p.worksCalls.Add(1)
	return p.stubMetaProvider.GetAuthorWorksByName(ctx, name)
}

type coreFixture struct {
	h       *AuthorHandler
	authors *db.AuthorRepo
	books   *db.BookRepo
	jobs    *jobs.Group
}

// newCoreFixture wires an AuthorHandler over a fresh in memory database with
// a jobs group, so a test can drain every catalogue sync the call started
// before it counts provider calls.
func newCoreFixture(t *testing.T, provider metadata.Provider) coreFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	group := jobs.NewGroup(context.Background())
	t.Cleanup(func() { group.Shutdown(5 * time.Second) })
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil,
		metadata.NewAggregator(provider), nil, profileRepo, nil).WithJobs(group)
	return coreFixture{h: h, authors: authorRepo, books: bookRepo, jobs: group}
}

// drain waits for every job the handler started and fails on a straggler.
func (f coreFixture) drain(t *testing.T) {
	t.Helper()
	if left := f.jobs.Shutdown(5 * time.Second); len(left) != 0 {
		t.Fatalf("jobs still running after drain: %v", left)
	}
}

func seedWells(t *testing.T, authors *db.AuthorRepo) *models.Author {
	t.Helper()
	author := &models.Author{
		ForeignID: "OL39307A", Name: "H. G. Wells", SortName: "Wells, H. G.",
		MetadataProvider: "openlibrary", Monitored: true,
		MonitorMode: models.AuthorMonitorModeAll,
	}
	if err := authors.Create(context.Background(), author); err != nil {
		t.Fatal(err)
	}
	return author
}

var wellsAddBookParams = addBookParams{
	ForeignBookID:   "OL27482W",
	ForeignAuthorID: "OL39307A",
	AuthorName:      "H. G. Wells",
}

// Default params must give what the handler gives for the same request: the
// same row, monitored, filed under a newly created author.
func TestAddBookCore_DefaultParamsMatchHandler(t *testing.T) {
	viaHandler := newCoreFixture(t, addBookBackCatalogueStub(false))
	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   wellsAddBookParams.ForeignBookID,
		"foreignAuthorId": wellsAddBookParams.ForeignAuthorID,
		"authorName":      wellsAddBookParams.AuthorName,
	})
	rec := httptest.NewRecorder()
	viaHandler.h.AddBook(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("handler: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var want models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}

	viaCore := newCoreFixture(t, addBookBackCatalogueStub(false))
	res, err := viaCore.h.addBookCore(context.Background(), wellsAddBookParams)
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	got := res.Book
	if got == nil {
		t.Fatal("core returned no book")
	}
	if got.ForeignID != want.ForeignID || got.Title != want.Title || got.Monitored != want.Monitored ||
		got.MediaType != want.MediaType || got.Status != want.Status || got.OwnerUserID != want.OwnerUserID {
		t.Errorf("core book = %+v\nhandler book = %+v", *got, want)
	}
	if !got.Monitored {
		t.Error("default params must leave the book monitored")
	}
	if !res.BookCreated || !res.AuthorCreated {
		t.Errorf("BookCreated=%v AuthorCreated=%v, want both true for a new author and book", res.BookCreated, res.AuthorCreated)
	}
	if res.Author == nil || res.Author.ForeignID != "OL39307A" || got.AuthorID != res.Author.ID {
		t.Errorf("result author = %+v, book author id %d", res.Author, got.AuthorID)
	}
	stored, err := viaCore.books.GetByForeignID(context.Background(), "OL27482W")
	if err != nil || stored == nil || stored.ID != got.ID || !stored.Monitored {
		t.Errorf("stored row = %+v err=%v, want the returned monitored row", stored, err)
	}

	// A second add of the same book is refused with the existing row, which
	// the handler turns into its 409.
	_, err = viaCore.h.addBookCore(context.Background(), wellsAddBookParams)
	var inLibrary *bookInLibraryError
	if !errors.As(err, &inLibrary) || inLibrary.Book == nil || inLibrary.Book.ID != got.ID {
		t.Fatalf("re-add error = %v, want bookInLibraryError for row %d", err, got.ID)
	}
	rec = httptest.NewRecorder()
	viaCore.h.writeAddBookError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", nil), err)
	if rec.Code != http.StatusConflict {
		t.Errorf("mapped re-add status = %d, want 409", rec.Code)
	}
}

func TestAddBookCore_ExistingAuthorIsNotReportedCreated(t *testing.T) {
	f := newCoreFixture(t, addBookBackCatalogueStub(false))
	author := seedWells(t, f.authors)

	res, err := f.h.addBookCore(context.Background(), wellsAddBookParams)
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorCreated {
		t.Error("AuthorCreated = true for an author already in the library")
	}
	if !res.BookCreated {
		t.Error("BookCreated = false for a book the direct insert created")
	}
	if res.Author == nil || res.Author.ID != author.ID {
		t.Errorf("result author = %+v, want id %d", res.Author, author.ID)
	}
}

func TestAddBookCore_MonitoredFalseCreatesUnmonitoredBook(t *testing.T) {
	f := newCoreFixture(t, addBookBackCatalogueStub(false))
	p := wellsAddBookParams
	unmonitored := false
	p.Monitored = &unmonitored

	res, err := f.h.addBookCore(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Book.Monitored {
		t.Error("returned book is monitored, want unmonitored")
	}
	stored, err := f.books.GetByForeignID(context.Background(), "OL27482W")
	if err != nil || stored == nil {
		t.Fatalf("book not stored: err=%v", err)
	}
	if stored.Monitored {
		t.Error("stored book is monitored, want unmonitored")
	}
	if !res.BookCreated {
		t.Error("BookCreated = false for a newly inserted book")
	}
}

// When the direct insert fails, the default path falls back to a single work
// catalogue sync. SkipCatalogueSync must start no sync and answer at once
// instead of polling the full 15 seconds.
func TestAddBookCore_SkipCatalogueSyncFiresNoFetch(t *testing.T) {
	t.Run("default runs the fallback sync", func(t *testing.T) {
		provider := &worksCountingProvider{stubMetaProvider: addBookBackCatalogueStub(true)}
		f := newCoreFixture(t, provider)
		seedWells(t, f.authors)

		res, err := f.h.addBookCore(context.Background(), wellsAddBookParams)
		if err != nil {
			t.Fatalf("expected the fallback to create the book, got %v", err)
		}
		f.drain(t)
		if provider.worksCalls.Load() == 0 {
			t.Error("fallback made no author works call; the control case proves nothing")
		}
		if !res.BookCreated {
			t.Error("BookCreated = false for a book the fallback sync created")
		}
	})

	t.Run("skip starts no sync", func(t *testing.T) {
		provider := &worksCountingProvider{stubMetaProvider: addBookBackCatalogueStub(true)}
		f := newCoreFixture(t, provider)
		seedWells(t, f.authors)
		p := wellsAddBookParams
		p.SkipCatalogueSync = true

		start := time.Now()
		_, err := f.h.addBookCore(context.Background(), p)
		elapsed := time.Since(start)
		if !errors.Is(err, errAddBookNotFound) {
			t.Fatalf("err = %v, want errAddBookNotFound", err)
		}
		f.drain(t)
		if n := provider.worksCalls.Load(); n != 0 {
			t.Errorf("author works calls = %d, want 0", n)
		}
		if got, _ := f.books.GetByForeignID(context.Background(), "OL27482W"); got != nil {
			t.Errorf("book created without a sync: %+v", got)
		}
		if elapsed > 5*time.Second {
			t.Errorf("skip path took %v; it should not poll", elapsed)
		}
	})
}

func wellsCreateProvider() *worksCountingProvider {
	stub := addBookBackCatalogueStub(false)
	stub.author = &models.Author{
		ForeignID: "OL39307A", Name: "H. G. Wells", SortName: "Wells, H. G.",
		MetadataProvider: "openlibrary",
	}
	return &worksCountingProvider{stubMetaProvider: stub}
}

var wellsCreateParams = createAuthorParams{
	ForeignID: "OL39307A",
	Name:      "H. G. Wells",
	Monitored: true,
}

func TestCreateAuthorCore_DefaultParamsMatchHandler(t *testing.T) {
	viaHandler := newCoreFixture(t, wellsCreateProvider())
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": wellsCreateParams.ForeignID,
		"authorName":      wellsCreateParams.Name,
		"monitored":       wellsCreateParams.Monitored,
	})
	rec := httptest.NewRecorder()
	viaHandler.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("handler: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var want models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	viaHandler.drain(t)

	provider := wellsCreateProvider()
	viaCore := newCoreFixture(t, provider)
	res, err := viaCore.h.createAuthorCore(context.Background(), wellsCreateParams)
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	viaCore.drain(t)
	got := res.Author
	if !res.Created || got == nil {
		t.Fatalf("result = %+v, want a created author", res)
	}
	if got.ForeignID != want.ForeignID || got.Name != want.Name || got.Monitored != want.Monitored ||
		got.MonitorMode != want.MonitorMode || got.MonitorNewItems != want.MonitorNewItems {
		t.Errorf("core author = %+v\nhandler author = %+v", *got, want)
	}
	if provider.worksCalls.Load() == 0 {
		t.Error("default params started no catalogue sync")
	}

	// Adding the same author again is the handler's 409 with the canonical row.
	_, err = viaCore.h.createAuthorCore(context.Background(), wellsCreateParams)
	var conflict *authorConflictError
	if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != got.ID {
		t.Fatalf("re-create error = %v, want authorConflictError for author %d", err, got.ID)
	}
}

func TestCreateAuthorCore_SkipCatalogueSyncFiresNoFetch(t *testing.T) {
	provider := wellsCreateProvider()
	f := newCoreFixture(t, provider)
	p := wellsCreateParams
	p.SkipCatalogueSync = true

	res, err := f.h.createAuthorCore(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.drain(t)
	if !res.Created {
		t.Error("Created = false, want a new author")
	}
	if n := provider.worksCalls.Load(); n != 0 {
		t.Errorf("author works calls = %d, want 0", n)
	}
	books, err := f.books.ListByAuthor(context.Background(), res.Author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Errorf("skip created %d books, want 0", len(books))
	}
}

// The handler maps every sentinel to the status and body it answered with
// before the split. This pins the table against the literal bodies.
func TestWriteAddBookError_Statuses(t *testing.T) {
	h := &AuthorHandler{}
	cases := []struct {
		err    error
		status int
		body   string
	}{
		{errAddBookForeignBookIDRequired, http.StatusBadRequest, "foreignBookId required"},
		{errAddBookInvalidMediaType, http.StatusBadRequest, "mediaType must be 'ebook', 'audiobook', or 'both'"},
		{errAddBookAuthorExists, http.StatusConflict, "author already exists"},
		{errAddBookAuthorUnresolved, http.StatusInternalServerError, "could not resolve author"},
		{errAddBookCancelled, http.StatusGatewayTimeout, "request cancelled"},
		{errAddBookHeldByAnotherUser, http.StatusConflict, "book is held by another user"},
		{&addBookLookupError{Err: errors.New("look up book metadata: boom")}, http.StatusBadGateway, "look up book metadata: boom"},
		{errors.New("disk on fire"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.writeAddBookError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", nil), tc.err)
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: body %q: %v", tc.err, rec.Body.String(), err)
		}
		if rec.Code != tc.status || got["error"] != tc.body {
			t.Errorf("%v: got %d %v, want %d %q", tc.err, rec.Code, got["error"], tc.status, tc.body)
		}
	}
}
