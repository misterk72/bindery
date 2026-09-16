package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// outageRows is the size of the import each #2613 test runs. It only has to
// be comfortably larger than the streak that stops the lookups.
const outageRows = 20

// maxPrimaryCallsDuringOutage is how often an import may ask a primary that
// never answers before it stops asking: the streak itself, plus one lookup of
// slack so the tests pin the shape of the fix rather than its exact count.
const maxPrimaryCallsDuringOutage = primaryOutageThreshold + 1

// countingDownPrimary is an OpenLibrary primary that fails every lookup the
// way a black holed host does, counting how often it was asked. In
// production each of those calls costs the full provider timeout, so the
// count is the import's running time in units of that timeout.
func countingDownPrimary(calls *atomic.Int32) *stubProvider {
	fail := func() error {
		calls.Add(1)
		return errOpenLibraryTimeout
	}
	return &stubProvider{
		name:            "openlibrary",
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) { return nil, fail() },
		getBookByISBNFn: func(context.Context, string) (*models.Book, error) { return nil, fail() },
		searchBooksFn:   func(context.Context, string) ([]models.Book, error) { return nil, fail() },
	}
}

func outageAuthorNames() []string {
	names := make([]string, outageRows)
	for i := range names {
		names[i] = fmt.Sprintf("Outage Author %02d", i+1)
	}
	return names
}

// assertAllFailedPrimaryDown checks every name failed with a reason that says
// the primary did not answer and that nothing was created.
func assertAllFailedPrimaryDown(t *testing.T, res *Result, repo *db.AuthorRepo, names []string) {
	t.Helper()
	if res.Added != 0 || res.Errors != len(names) {
		t.Errorf("Added=%d Errors=%d; want 0/%d (failures=%v)", res.Added, res.Errors, len(names), res.Failures)
	}
	for _, name := range names {
		if msg := res.Failures[name]; !strings.Contains(msg, "openlibrary did not answer") {
			t.Errorf("row %q reason = %q, want it to say the primary did not answer", name, msg)
		}
	}
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("created %d authors while the primary was down, want 0", len(all))
	}
}

func assertPrimaryCallsBounded(t *testing.T, calls *atomic.Int32, started time.Time) {
	t.Helper()
	if got := calls.Load(); got > maxPrimaryCallsDuringOutage {
		t.Errorf("primary asked %d times for %d rows, want at most %d: every extra call is a full provider timeout",
			got, outageRows, maxPrimaryCallsDuringOutage)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("import took %s against stubs, want it fast", elapsed)
	}
}

// TestImportCSVAuthors_PrimaryOutageStopsAsking is #2613 on the CSV import:
// with the primary down and a fallback answering, every row used to wait out
// the primary's timeout only to be refused by the #2332 guard. After a short
// streak of unanswered lookups the rest of the rows must fail at once.
func TestImportCSVAuthors_PrimaryOutageStopsAsking(t *testing.T) {
	var calls atomic.Int32
	agg := metadata.NewAggregator(countingDownPrimary(&calls), answeringProvider("dnb", dnbAuthorID))
	repo := db.NewAuthorRepo(newTestDB(t))
	names := outageAuthorNames()

	started := time.Now()
	res, err := ImportCSVAuthors(context.Background(), strings.NewReader(strings.Join(names, "\n")), repo, nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	assertPrimaryCallsBounded(t, &calls, started)
	assertAllFailedPrimaryDown(t, res, repo, names)
}

// TestImportReadarr_PrimaryOutageStopsAsking: the Readarr import shares
// resolveAndCreateAuthor with CSV and must stop asking the same way.
func TestImportReadarr_PrimaryOutageStopsAsking(t *testing.T) {
	var calls atomic.Int32
	agg := metadata.NewAggregator(countingDownPrimary(&calls), answeringProvider("dnb", dnbAuthorID))
	names := outageAuthorNames()

	path := newReadarrDB(t)
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if _, err := src.Exec(`INSERT INTO AuthorMetadata (Id, Name) VALUES (?, ?)`, i+1, name); err != nil {
			t.Fatal(err)
		}
		if _, err := src.Exec(`INSERT INTO Authors (Monitored, AuthorMetadataId) VALUES (1, ?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	src.Close()

	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	started := time.Now()
	res, err := ImportReadarr(context.Background(), path, repo, db.NewIndexerRepo(database),
		db.NewDownloadClientRepo(database), db.NewBlocklistRepo(database), nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}
	assertPrimaryCallsBounded(t, &calls, started)
	authors := res.Authors
	assertAllFailedPrimaryDown(t, &authors, repo, names)
}

// TestImportCSVAuthors_PrimaryAnswerResetsOutageStreak: a primary that drops
// the odd lookup but answers in between is not down, so the import must keep
// asking it for every row rather than giving up on the whole run.
func TestImportCSVAuthors_PrimaryAnswerResetsOutageStreak(t *testing.T) {
	var calls atomic.Int32
	primary := &stubProvider{
		name: "openlibrary",
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) {
			// Every third lookup answers (with nothing), so the streak of
			// failures never gets past two.
			if calls.Add(1)%primaryOutageThreshold == 0 {
				return nil, nil
			}
			return nil, errOpenLibraryTimeout
		},
	}
	agg := metadata.NewAggregator(primary, failingProvider("dnb", nil))
	repo := db.NewAuthorRepo(newTestDB(t))
	names := outageAuthorNames()

	res, err := ImportCSVAuthors(context.Background(), strings.NewReader(strings.Join(names, "\n")), repo, nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if got := calls.Load(); got != outageRows {
		t.Errorf("primary asked %d times, want once per row (%d): an answering primary must reset the streak", got, outageRows)
	}
	if res.Errors != outageRows {
		t.Errorf("Errors=%d, want %d", res.Errors, outageRows)
	}
}

// TestGoodreadsImport_PrimaryOutageStopsAsking is #2613 on the Goodreads
// import, whose ISBN walk has no per provider cap: each lookup against a
// black holed OpenLibrary costs about a minute, and a row makes two or three.
func TestGoodreadsImport_PrimaryOutageStopsAsking(t *testing.T) {
	var calls atomic.Int32
	agg := metadata.NewAggregator(countingDownPrimary(&calls), answeringProvider("dnb", dnbAuthorID))
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	rows := make([]GoodreadsRow, outageRows)
	for i := range rows {
		rows[i] = GoodreadsRow{
			RowNumber:      i + 1,
			Title:          fmt.Sprintf("Outage Book %02d", i+1),
			Author:         guardAuthorName,
			ISBN13:         fmt.Sprintf("97805931352%02d", i),
			ISBN:           fmt.Sprintf("05931352%02d", i),
			ExclusiveShelf: GoodreadsShelfToRead,
		}
	}

	started := time.Now()
	resolved := ResolveGoodreadsRows(ctx, rows, GoodreadsImportOptions{}, agg, books, 0)
	assertPrimaryCallsBounded(t, &calls, started)

	if len(resolved) != outageRows {
		t.Fatalf("rows = %d, want %d", len(resolved), outageRows)
	}
	for _, rr := range resolved {
		if rr.Outcome != outcomeUnresolved || !strings.Contains(rr.Reason, "openlibrary did not answer") {
			t.Errorf("row %d: outcome=%q reason=%q, want unresolved because the primary did not answer",
				rr.Row.RowNumber, rr.Outcome, rr.Reason)
		}
	}
	if commit := CommitGoodreadsImport(ctx, resolved, authors, nil, books); commit.Added != 0 {
		t.Errorf("committed %d books, want 0", commit.Added)
	}
	assertNoAuthorBound(t, authors, dnbAuthorID)
}

// TestGoodreadsImport_PrimaryOutageTripsMidRow: a Goodreads row makes up to
// three lookups, and the streak counts lookups, not rows. When the streak
// trips part way through a row the row's remaining lookups are skipped and
// it still says the primary did not answer rather than blaming the ISBN.
func TestGoodreadsImport_PrimaryOutageTripsMidRow(t *testing.T) {
	var calls atomic.Int32
	agg := metadata.NewAggregator(countingDownPrimary(&calls), failingProvider("dnb", nil))

	rows := []GoodreadsRow{
		{RowNumber: 1, Title: "No ISBN", Author: guardAuthorName, ExclusiveShelf: GoodreadsShelfToRead},
		{RowNumber: 2, Title: "Both ISBNs", Author: guardAuthorName, ISBN13: "9780593135204", ISBN: "0593135202", ExclusiveShelf: GoodreadsShelfToRead},
		{RowNumber: 3, Title: "Later row", Author: guardAuthorName, ISBN13: "9780593135211", ExclusiveShelf: GoodreadsShelfToRead},
	}
	resolved := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, agg, nil, 0)

	// Row 1's title search, then row 2's two ISBN lookups trip the streak,
	// so row 2's title search and all of row 3 are never asked.
	if got := calls.Load(); got != primaryOutageThreshold {
		t.Errorf("primary asked %d times, want %d", got, primaryOutageThreshold)
	}
	for _, rr := range resolved {
		if rr.Outcome != outcomeUnresolved || rr.Reason != wantPrimaryDownReason {
			t.Errorf("row %d: outcome=%q reason=%q, want unresolved with %q",
				rr.Row.RowNumber, rr.Outcome, rr.Reason, wantPrimaryDownReason)
		}
	}
}
