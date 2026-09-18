package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// seriesLinkFixture stands up the shape #2328 is about: an author whose books
// are all already in the library (the ABS or calibre import case) and whose
// monitoring refuses newly discovered works, so a refresh may update rows and
// may not create them.
type seriesLinkFixture struct {
	db      *sql.DB
	authors *db.AuthorRepo
	books   *db.BookRepo
	series  *db.SeriesRepo
	profile *db.MetadataProfileRepo
	author  *models.Author
}

func newSeriesLinkFixture(t *testing.T, acceptsNewBooks bool) *seriesLinkFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &seriesLinkFixture{
		db:      database,
		authors: db.NewAuthorRepo(database),
		books:   db.NewBookRepo(database),
		series:  db.NewSeriesRepo(database),
		profile: db.NewMetadataProfileRepo(database),
	}
	f.author = &models.Author{
		ForeignID: "OL2236A", Name: "Ann Leckie", SortName: "Leckie, Ann",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if !acceptsNewBooks {
		f.author.MonitorNewItems = models.AuthorMonitorNewItemsNone
	}
	if err := f.authors.Create(context.Background(), f.author); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *seriesLinkFixture) handler(stub *stubMetaProvider) *AuthorHandler {
	return NewAuthorHandler(f.authors, nil, f.books, f.series,
		metadata.NewAggregator(stub), nil, f.profile, nil)
}

// addImportedBook stores a book the way an import leaves it: a real row with
// no series membership of any kind.
func (f *seriesLinkFixture) addImportedBook(t *testing.T, foreignID, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: f.author.ID, Title: title, SortTitle: title,
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

// seriesWork is a provider work that names the series it belongs to, which is
// what every real provider returns and what the create path already reads.
func seriesWork(foreignID, title, position string) models.Book {
	b := models.Book{
		ForeignID: foreignID, Title: title, SortTitle: title,
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
		SeriesRefs: []models.SeriesRef{{
			ForeignID: "OL-S-IMPERIAL-RADCH", Title: "Imperial Radch", Position: position, Primary: true,
		}},
	}
	return b
}

type storedLink struct {
	seriesTitle string
	position    string
	primary     bool
}

// linksForBook reads series_books directly. The point of these tests is the
// rows, including how many of them there are, so they do not go through a
// repo helper that could hide a duplicate.
func linksForBook(t *testing.T, f *seriesLinkFixture, bookID int64) []storedLink {
	t.Helper()
	rows, err := f.db.QueryContext(context.Background(), `
		SELECT s.title, sb.position_in_series, sb.primary_series
		FROM series_books sb JOIN series s ON s.id = sb.series_id
		WHERE sb.book_id = ? ORDER BY s.title`, bookID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedLink
	for rows.Next() {
		var l storedLink
		var primary int
		if err := rows.Scan(&l.seriesTitle, &l.position, &primary); err != nil {
			t.Fatal(err)
		}
		l.primary = primary != 0
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, f *seriesLinkFixture, query string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func refreshCatalogue(t *testing.T, h *AuthorHandler, author *models.Author) {
	t.Helper()
	if _, err := h.runCatalogueSync(context.Background(), author, catalogueSyncOptions{
		mediaType: models.MediaTypeEbook, discovery: true,
	}); err != nil {
		t.Fatalf("catalogue sync: %v", err)
	}
}

// The headline case (#2328). Every book of an imported library already exists,
// none is in a series, and the author refuses new books. Before the fix the
// refresh updated ratings and covers and linked nothing, because series
// membership was only ever written for books the sync created.
func TestCatalogueSync_LinksSeriesOntoBooksThatAlreadyExist(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	justice := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	sword := f.addImportedBook(t, "OL2236W2", "Ancillary Sword")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
		seriesWork("OL2236W2", "Ancillary Sword", "2"),
	}})

	refreshCatalogue(t, h, f.author)

	for _, tc := range []struct {
		book     *models.Book
		position string
	}{{justice, "1"}, {sword, "2"}} {
		links := linksForBook(t, f, tc.book.ID)
		if len(links) != 1 {
			t.Fatalf("%s: got %d series links, want 1: %+v", tc.book.Title, len(links), links)
		}
		if links[0].seriesTitle != "Imperial Radch" || links[0].position != tc.position {
			t.Fatalf("%s: got %+v, want Imperial Radch at %q", tc.book.Title, links[0], tc.position)
		}
		if !links[0].primary {
			t.Fatalf("%s: the only series a book is in should be its primary one", tc.book.Title)
		}
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM books"); n != 2 {
		t.Fatalf("the refresh created books it was not allowed to create: %d rows, want 2", n)
	}
}

// The other existing-row branch: the work resolves by title, not by id. A
// calibre stub carries "calibre:" ids that no provider work will ever match,
// so this is the branch an imported library lands in most often.
func TestCatalogueSync_LinksSeriesOntoATitleMatchedRow(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	stub := f.addImportedBook(t, "calibre:77", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, stub.ID)
	if len(links) != 1 || links[0].seriesTitle != "Imperial Radch" || links[0].position != "1" {
		t.Fatalf("title matched row: got %+v, want one Imperial Radch link at position 1", links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM books"); n != 1 {
		t.Fatalf("the title match minted a second row: %d books, want 1", n)
	}
}

// A link that is already there is left exactly as it is, including a position
// that disagrees with the provider. Position is user editable and the renamer
// reads it; a refresh rewriting it would undo hand corrections silently.
func TestCatalogueSync_LeavesAnExistingSeriesLinkAlone(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	s := &models.Series{ForeignID: "OL-S-IMPERIAL-RADCH", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, s.ID, book.ID, "1.5", false); err != nil {
		t.Fatal(err)
	}
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, book.ID)
	if len(links) != 1 {
		t.Fatalf("got %d links, want the one that was already there: %+v", len(links), links)
	}
	if links[0].position != "1.5" {
		t.Fatalf("stored position = %q, want it untouched at 1.5", links[0].position)
	}
	if links[0].primary {
		t.Fatal("the refresh promoted a link the user had demoted")
	}
}

// The two providers mint series ids in different namespaces, so the same
// series arrives under an id the stored link does not carry. Linking it would
// file one book under two series rows. The stored row wins.
func TestCatalogueSync_DoesNotRelinkTheSameSeriesUnderAnotherProviderID(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	stored := &models.Series{ForeignID: "ol-series:imperial-radch", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, stored.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs[0].ForeignID = "hc-series:1026"
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, book.ID)
	if len(links) != 1 {
		t.Fatalf("got %d links, want the one already stored: %+v", len(links), links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 1 {
		t.Fatalf("got %d series rows, want the one already stored", n)
	}
}

// Two refreshes in a row must leave one row, not two. series_books is keyed on
// (series_id, book_id), so this also pins the series upsert: a second series
// row with the same foreign id would have produced a second link.
func TestCatalogueSync_RepeatedRefreshDoesNotDuplicateSeriesRows(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)
	refreshCatalogue(t, h, f.author)
	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 1 {
		t.Fatalf("got %d series links after three refreshes, want 1: %+v", len(links), links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 1 {
		t.Fatalf("got %d series rows, want 1", n)
	}
}

// A provider that offers no series data changes nothing. This is also the
// no-cost path: with no ref to act on the linker never takes its one read.
func TestCatalogueSync_NoSeriesDataFromTheProviderChangesNothing(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs = nil
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 0 {
		t.Fatalf("a refresh with no provider series data invented links: %+v", links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 0 {
		t.Fatalf("got %d series rows, want none", n)
	}
}

// A ref with no foreign id is dropped rather than upserted: CreateOrGet
// refuses an empty foreign id (#1645) because every such caller would
// otherwise collapse onto one shared series row.
func TestCatalogueSync_SkipsASeriesRefWithNoForeignID(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs[0].ForeignID = ""
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 0 {
		t.Fatalf("an unidentified series ref was linked anyway: %+v", links)
	}
}

// The unattended discovery job calls the same sync path on a schedule. It must
// still create the work it found AND link the series of the books already
// there, and it must stop when its per author budget cancels the context.
func TestDiscoverAuthorBooks_LinksSeriesForExistingBooksAndStillCreates(t *testing.T) {
	f := newSeriesLinkFixture(t, true)
	existing := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
		seriesWork("OL2236W2", "Ancillary Sword", "2"),
	}})

	created, err := h.DiscoverAuthorBooks(context.Background(), f.author)
	if err != nil {
		t.Fatalf("DiscoverAuthorBooks: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want the one work the library did not have", created)
	}
	if links := linksForBook(t, f, existing.ID); len(links) != 1 || links[0].position != "1" {
		t.Fatalf("discovery did not link the series of the book that already existed: %+v", links)
	}
	fresh, err := f.books.GetByForeignID(context.Background(), "OL2236W2")
	if err != nil || fresh == nil {
		t.Fatalf("discovery did not create the new work: %v", err)
	}
	if links := linksForBook(t, f, fresh.ID); len(links) != 1 {
		t.Fatalf("the created book lost its series link: %+v", links)
	}
}

// A cancelled context stops the linker instead of running every remaining ref
// against a dead connection. The discovery job bounds each author with a
// context deadline, and that is how it reaches this code.
func TestExistingBookSeriesLinker_StopsOnACancelledContext(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	linker := newExistingBookSeriesLinker(f.series, f.author.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	linker.link(ctx, book, seriesWork("OL2236W1", "Ancillary Justice", "1").SeriesRefs)

	if linker.linked != 0 {
		t.Fatalf("linked = %d, want nothing written after the budget ran out", linker.linked)
	}
}
