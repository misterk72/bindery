package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

// TestRecoverStaleClaims_ReversesADeadAdopt: the request dies (here a panic)
// after registering one file of two. The row still says adopting and records
// what was done; recovery reverses it exactly as Undo would and returns the
// unit to pending, instead of leaving a tracked file and a created book behind
// for the next scan to hide.
func TestRecoverStaleClaims_ReversesADeadAdopt(t *testing.T) {
	f := newAdoptionFixture(t, addBookBackCatalogueStub(false))
	ctx := context.Background()
	epub := f.write(t, "H. G. Wells/War of the Worlds.epub")
	mobi := f.write(t, "H. G. Wells/War of the Worlds.mobi")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub, mobi}})

	real := f.h.registerFile
	calls := 0
	f.h.registerFile = func(ctx context.Context, bookID int64, format, path string) (bool, error) {
		calls++
		if calls == 2 {
			panic("process killed mid adopt")
		}
		return real(ctx, bookID, format, path)
	}
	func() {
		defer func() { _ = recover() }()
		f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{
			"foreignBookId": "OL27482W", "foreignAuthorId": "OL39307A", "authorName": "H. G. Wells",
		})
	}()

	u, _ := f.units.Get(ctx, id)
	if u.State != db.UnmatchedStateAdopting || len(u.Registered) != 1 || u.CreatedBookID == 0 || u.CreatedAuthorID == 0 {
		t.Fatalf("row after the dead request = %+v, want adopting with one registered file and the created ids", u)
	}
	bookID, authorID := u.CreatedBookID, u.CreatedAuthorID

	// A fresh claim is not stale yet.
	if n, err := f.h.RecoverStaleClaims(ctx, db.UnmatchedClaimTimeout); err != nil || n != 0 {
		t.Fatalf("recovery of a fresh claim = %d %v, want 0", n, err)
	}
	n, err := f.h.RecoverStaleClaims(ctx, -time.Second)
	if err != nil || n != 1 {
		t.Fatalf("recovery = %d %v, want 1", n, err)
	}
	if owned, _ := f.books.PathOwnedByOtherBook(ctx, epub, 0); owned {
		t.Fatal("registered file still tracked after recovery")
	}
	if b, _ := f.books.GetByID(ctx, bookID); b != nil {
		t.Fatalf("created book left behind: %+v", b)
	}
	if a, _ := f.authors.GetByID(ctx, authorID); a != nil {
		t.Fatalf("created author left behind: %+v", a)
	}
	if u, _ := f.units.Get(ctx, id); u.State != db.UnmatchedStatePending || len(u.Registered) != 0 {
		t.Fatalf("row after recovery = %+v, want pending and clear", u)
	}
}

// refusingUndoStore makes CompleteUndo lose its compare and swap.
type refusingUndoStore struct{ *db.UnmatchedUnitRepo }

func (refusingUndoStore) CompleteUndo(context.Context, int64, string) (bool, error) {
	return false, nil
}

// TestUndo_ReportsALostCompletion: when the row stopped being held by this
// undo before it finished, the answer is a 409, not a success.
func TestUndo_ReportsALostCompletion(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Ancillary Mercy")
	path := f.write(t, "Ann Leckie/Ancillary Mercy.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID}); rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d", rec.Code)
	}
	f.h.units = refusingUndoStore{f.units}
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil); rec.Code != http.StatusConflict {
		t.Fatalf("undo with a lost completion = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// TestUndo_KeepsTheRecordWhenReversalFails: the database refuses one untrack
// (SQLITE_BUSY in production). Undo must not report success or clear the
// record while a file is still tracked: it answers 503 and keeps the claim,
// and the next recovery pass finishes the job.
func TestUndo_KeepsTheRecordWhenReversalFails(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	ctx := context.Background()
	book := f.seedBook(t, "Ancillary Justice")
	epub := f.write(t, "Ann Leckie/Ancillary Justice.epub")
	mobi := f.write(t, "Ann Leckie/Ancillary Justice.mobi")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub, mobi}})
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID}); rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}

	real := f.h.untrackFile
	f.h.untrackFile = func(ctx context.Context, path string, bookID int64) (bool, error) {
		if path == mobi {
			return false, errors.New("database is locked (SQLITE_BUSY)")
		}
		return real(ctx, path, bookID)
	}
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("undo with a failing untrack = %d %s, want 503", rec.Code, rec.Body.String())
	}
	u, _ := f.units.Get(ctx, id)
	if u.State != db.UnmatchedStateUndoing || len(u.Registered) != 2 {
		t.Fatalf("row after the failed undo = %+v, want the claim and both registered files on record", u)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 || got[0] != mobi {
		t.Fatalf("book files = %v, want the mobi still tracked", got)
	}

	f.h.untrackFile = real
	if n, err := f.h.RecoverStaleClaims(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("recovery = %d %v, want 1", n, err)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("book files after recovery = %v, want none", got)
	}
	if u, _ := f.units.Get(ctx, id); u.State != db.UnmatchedStatePending {
		t.Fatalf("row after recovery = %s, want pending", u.State)
	}
}

// TestAdopt_KeepsTheClaimWhenItsReversalFails: an adopt fails part way and
// then cannot untrack what it registered. It answers 503 and leaves the row
// claimed with its record, for recovery, instead of returning it to pending
// with a file still tracked and nothing saying so.
func TestAdopt_KeepsTheClaimWhenItsReversalFails(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	ctx := context.Background()
	book := f.seedBook(t, "Ancillary Sword")
	epub := f.write(t, "Ann Leckie/Ancillary Sword.epub")
	mobi := f.write(t, "Ann Leckie/Ancillary Sword.mobi")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub, mobi}})

	realRegister, realUntrack := f.h.registerFile, f.h.untrackFile
	f.h.registerFile = func(ctx context.Context, bookID int64, format, path string) (bool, error) {
		if path == mobi {
			return false, errors.New("disk on fire")
		}
		return realRegister(ctx, bookID, format, path)
	}
	f.h.untrackFile = func(context.Context, string, int64) (bool, error) {
		return false, errors.New("database is locked (SQLITE_BUSY)")
	}
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("adopt = %d %s, want 503", rec.Code, rec.Body.String())
	}
	u, _ := f.units.Get(ctx, id)
	if u.State != db.UnmatchedStateAdopting || len(u.Registered) != 1 {
		t.Fatalf("row = %+v, want the claim kept with the registered file on record", u)
	}

	f.h.untrackFile = realUntrack
	if n, err := f.h.RecoverStaleClaims(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("recovery = %d %v", n, err)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("book files after recovery = %v, want none", got)
	}
}
