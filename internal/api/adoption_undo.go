package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

// Undo, the compensation a failed adopt runs, and the recovery of claims a
// dead request left behind all reverse an adoption the same way: reverse.

// keptBookMessage is what Undo says when the created book stays.
const keptBookMessage = "The files are no longer adopted. The book it added stays in your library because it has been used since."

// Undo handles POST /library/unmatched/{id}/undo. It untracks exactly the
// book_files rows the adoption registered, each only while it still belongs
// to the book it was registered to, then removes a book or author the
// adoption created when nothing else holds it and nobody has used it since
// (S9). The unit returns to pending.
func (h *AdoptionHandler) Undo(w http.ResponseWriter, r *http.Request) {
	id, ok := unitIDParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	claimed, err := h.units.ClaimState(ctx, id, db.UnmatchedStateAdopted, db.UnmatchedStateUndoing)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !claimed {
		h.stateConflict(w, r, id)
		return
	}
	work := context.WithoutCancel(ctx)
	unit, err := h.units.Get(work, id)
	if err != nil || unit == nil {
		if _, rerr := h.units.ClaimState(work, id, db.UnmatchedStateUndoing, db.UnmatchedStateAdopted); rerr != nil {
			slog.Warn("adoption: could not release undo claim", "unit", id, "error", rerr)
		}
		if err == nil {
			err = refuse(http.StatusNotFound, "unmatched book not found")
		}
		h.writeAdoptionError(w, r, err)
		return
	}
	kept := h.reverse(work, id, recordOf(unit))
	done, err := h.units.CompleteUndo(work, id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !done {
		h.stateConflict(w, r, id)
		return
	}
	slog.Info("adoption: undone", "unit", id, "book_id", unit.BookID, "files", len(unit.Registered), "kept_created_book", kept)
	message := ""
	if kept {
		message = keptBookMessage
	}
	h.writeUnitWithMessage(w, r, id, message)
}

func recordOf(u *db.UnmatchedUnit) db.AdoptionRecord {
	return db.AdoptionRecord{
		BookID: u.BookID, CreatedBookID: u.CreatedBookID, CreatedAuthorID: u.CreatedAuthorID,
		CreatedBookFingerprint: u.CreatedBookFingerprint, Registered: u.Registered,
	}
}

// reverse undoes an adoption's writes and reports whether a created book was
// kept because it has been used since. Best effort; every step is logged.
//
// The fingerprint is compared before untracking, because untracking itself
// updates the book.
func (h *AdoptionHandler) reverse(ctx context.Context, unitID int64, rec db.AdoptionRecord) bool {
	used := false
	if rec.CreatedBookID > 0 && rec.CreatedBookFingerprint != "" {
		now, err := h.units.BookFingerprint(ctx, rec.CreatedBookID)
		used = err != nil || (now != "" && now != rec.CreatedBookFingerprint)
	}
	for _, f := range rec.Registered {
		if _, err := h.books.UntrackFilePathForBook(ctx, f.Path, f.BookID); err != nil {
			slog.Warn("adoption: could not untrack file", "path", f.Path, "book_id", f.BookID, "error", err)
		}
	}
	if used {
		return true
	}
	if rec.CreatedBookID > 0 && h.bookIsOnlyOurs(ctx, unitID, rec.CreatedBookID) {
		if err := h.books.Delete(ctx, rec.CreatedBookID); err != nil {
			slog.Warn("adoption: could not remove created book", "book_id", rec.CreatedBookID, "error", err)
		}
	}
	if rec.CreatedAuthorID > 0 {
		// Including excluded books: deleting the author cascades to them.
		books, err := h.books.ListByAuthorIncludingExcluded(ctx, rec.CreatedAuthorID)
		if err != nil || len(books) > 0 {
			return false
		}
		if other, err := h.units.AuthorReferencedElsewhere(ctx, rec.CreatedAuthorID, unitID); err != nil || other {
			return false
		}
		if err := h.authors.Delete(ctx, rec.CreatedAuthorID); err != nil {
			slog.Warn("adoption: could not remove created author", "author_id", rec.CreatedAuthorID, "error", err)
		}
	}
	return false
}

func (h *AdoptionHandler) bookIsOnlyOurs(ctx context.Context, unitID, bookID int64) bool {
	files, err := h.books.ListFiles(ctx, bookID)
	if err != nil || len(files) > 0 {
		return false
	}
	other, err := h.units.BookReferencedElsewhere(ctx, bookID, unitID)
	return err == nil && !other
}

// RecoverStaleClaims reverses and releases rows an adopt or undo claimed
// longer than olderThan ago: requests that died holding them. The row records
// every side effect before the next one, so the reversal is the same one Undo
// runs, with the same limits. Call it with zero at startup, when any claim is
// from a previous process, and with db.UnmatchedClaimTimeout later.
func (h *AdoptionHandler) RecoverStaleClaims(ctx context.Context, olderThan time.Duration) (int, error) {
	stale, err := h.units.StaleClaims(ctx, time.Now().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	recovered := 0
	for i := range stale {
		u := &stale[i]
		h.reverse(ctx, u.ID, recordOf(u))
		ok, err := h.units.ResetToPending(ctx, u.ID, u.State)
		if err != nil {
			return recovered, err
		}
		if ok {
			recovered++
			slog.Warn("adoption: recovered an abandoned claim", "unit", u.ID, "state", u.State, "files", len(u.Registered))
		}
	}
	return recovered, nil
}
