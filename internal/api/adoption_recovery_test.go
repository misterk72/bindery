package api

import (
	"context"
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

func (refusingUndoStore) CompleteUndo(context.Context, int64) (bool, error) { return false, nil }

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
