package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// adoptRequest names the book a unit is, one of two ways: a book already in
// the library (bookId), or a metadata result to add (foreignBookId with its
// author). There is no path field; the unit's files come from its row.
type adoptRequest struct {
	BookID          int64  `json:"bookId"`
	ForeignBookID   string `json:"foreignBookId"`
	ForeignAuthorID string `json:"foreignAuthorId"`
	AuthorName      string `json:"authorName"`
	// Format optionally overrides the format the scan detected.
	Format string `json:"format"`
}

// Adopt handles POST /library/unmatched/{id}/adopt.
//
// Adoption is the scanner's own reconcile with a person supplying the match:
// the unit's files are registered in place with the same book_files write the
// scan uses (AddBookFileIfMissing is AddBookFile that also reports whether it
// inserted the row, which is what makes Undo exact). Nothing is moved, renamed
// or queued.
//
// A book added from metadata is created unmonitored and with its media type
// set to the adopted format, so a default of "both" cannot leave the other
// format Wanted and have it grabbed unasked. SkipCatalogueSync is set: the add
// never pulls the author's bibliography anyway (#1816), and skipping the
// single work fallback makes a provider failure fail at once instead of
// polling for 15 seconds, and means no background sync can create the book
// after this request has already given up on it.
//
// Atomicity is by compensation. The row is claimed first (S9); every file is
// checked on disk and against the library roots before anything is written
// (S10); if registering fails part way, what was registered is untracked and
// a book or author this request created is removed again.
func (h *AdoptionHandler) Adopt(w http.ResponseWriter, r *http.Request) {
	id, ok := unitIDParam(w, r)
	if !ok {
		return
	}
	var req adoptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.ForeignBookID = strings.TrimSpace(req.ForeignBookID)
	if (req.BookID > 0) == (req.ForeignBookID != "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send either bookId or foreignBookId"})
		return
	}
	switch req.Format {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be 'ebook' or 'audiobook'"})
		return
	}

	ctx := r.Context()
	claimed, err := h.units.ClaimState(ctx, id, db.UnmatchedStatePending, db.UnmatchedStateAdopting)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !claimed {
		h.stateConflict(w, r, id)
		return
	}
	// The request context may be cancelled once work has started; releasing
	// the claim and compensating must still happen.
	work := context.WithoutCancel(ctx)
	if err := h.adopt(work, id, req); err != nil {
		if _, rerr := h.units.ClaimState(work, id, db.UnmatchedStateAdopting, db.UnmatchedStatePending); rerr != nil {
			slog.Warn("adoption: could not release claim", "unit", id, "error", rerr)
		}
		h.writeAdoptionError(w, r, err)
		return
	}
	h.writeUnit(w, r, id)
}

func (h *AdoptionHandler) adopt(ctx context.Context, id int64, req adoptRequest) error {
	unit, err := h.units.Get(ctx, id)
	if err != nil {
		return err
	}
	if unit == nil {
		return refuse(http.StatusNotFound, "unmatched book not found")
	}
	format := req.Format
	if format == "" {
		format = unit.Format
	}
	paths, err := h.registrationPaths(ctx, unit, format)
	if err != nil {
		return err
	}
	// Refuse before any side effect when a file already belongs to a book.
	if err := h.checkOwnership(ctx, append(paths, unit.MemberPaths...), 0); err != nil {
		return err
	}

	var book *models.Book
	var created addBookResult
	if req.BookID > 0 {
		if book, err = h.books.GetByID(ctx, req.BookID); err != nil {
			return err
		}
		if book == nil {
			return refuse(http.StatusNotFound, "That book is no longer in your library.")
		}
	} else {
		if h.adder == nil {
			return refuse(http.StatusServiceUnavailable, "Adding books is not available.")
		}
		unmonitored := false
		created, err = h.adder.addBookCore(ctx, addBookParams{
			ForeignBookID:     req.ForeignBookID,
			ForeignAuthorID:   strings.TrimSpace(req.ForeignAuthorID),
			AuthorName:        strings.TrimSpace(req.AuthorName),
			SearchOnAdd:       false,
			MediaType:         format,
			Monitored:         &unmonitored,
			SkipCatalogueSync: true,
		})
		var inLibrary *bookInLibraryError
		switch {
		case errors.As(err, &inLibrary):
			// Already in the library: adopt into that row, as if picked.
			book = inLibrary.Book
		case err != nil:
			return err
		default:
			book = created.Book
		}
	}

	rec := db.AdoptionRecord{BookID: book.ID}
	if created.BookCreated {
		rec.CreatedBookID = book.ID
	}
	if created.AuthorCreated && created.Author != nil {
		rec.CreatedAuthorID = created.Author.ID
	}

	registered, regErr := h.register(ctx, book.ID, format, paths, unit.MemberPaths)
	if rec.CreatedBookID > 0 {
		// A book this request created may also have picked up a file from
		// the add's own library lookup. Everything on it came from this
		// request, so Undo, or the compensation below, takes all of it.
		if files, err := h.books.ListFiles(ctx, book.ID); err != nil {
			if regErr == nil {
				regErr = err
			}
		} else {
			registered = make([]string, 0, len(files))
			for _, f := range files {
				registered = append(registered, f.Path)
			}
		}
	}
	rec.RegisteredPaths = registered
	if regErr != nil {
		h.reverse(ctx, id, rec)
		return regErr
	}
	done, err := h.units.CompleteAdoption(ctx, id, rec)
	if err != nil || !done {
		h.reverse(ctx, id, rec)
		if err == nil {
			err = refuse(http.StatusConflict, "This book changed while it was being adopted. Try again.")
		}
		return err
	}
	slog.Info("adoption: registered files in place", "unit", id, "book_id", book.ID,
		"files", len(registered), "book_created", rec.CreatedBookID > 0, "author_created", rec.CreatedAuthorID > 0)
	return nil
}

// registrationPaths decides what goes into book_files for a unit and checks
// every file first. An audiobook folder is registered as its folder, the way
// an imported audiobook download is (SetFormatFilePath with the folder); the
// scan counts every track beneath it as tracked. Anything else registers each
// member file.
//
// Each member must still be a regular file (Lstat, so a symlink swapped in
// since the scan is refused) and must resolve inside a library root: the row
// may be hours old (S10). The path registered is the one the scan walked, so
// the next scan recognises it.
func (h *AdoptionHandler) registrationPaths(ctx context.Context, unit *db.UnmatchedUnit, format string) ([]string, error) {
	if len(unit.MemberPaths) == 0 {
		return nil, refuse(http.StatusUnprocessableEntity, "This book has no files left. Scan again to refresh the list.")
	}
	for _, p := range unit.MemberPaths {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "A file of this book is gone: "+filepath.Base(p)+". Scan again to refresh the list.")
		}
		if !info.Mode().IsRegular() {
			return nil, refuse(http.StatusUnprocessableEntity, filepath.Base(p)+" is not a regular file, so it cannot be adopted.")
		}
		if _, ok := h.roots.ResolveContained(ctx, p); !ok {
			return nil, refuse(http.StatusUnprocessableEntity, filepath.Base(p)+" is outside your library folders, so it cannot be adopted.")
		}
	}
	if unit.UnitKind == db.UnmatchedKindFolder && unit.Format == models.MediaTypeAudiobook && format == models.MediaTypeAudiobook {
		if _, ok := h.roots.ResolveContained(ctx, unit.UnitPath); !ok {
			return nil, refuse(http.StatusUnprocessableEntity, "This folder is outside your library folders, so it cannot be adopted.")
		}
		return []string{filepath.Clean(unit.UnitPath)}, nil
	}
	out := make([]string, len(unit.MemberPaths))
	for i, p := range unit.MemberPaths {
		out[i] = filepath.Clean(p)
	}
	return out, nil
}

// checkOwnership refuses when any path already belongs to a book other than
// bookID (0 means any book at all).
func (h *AdoptionHandler) checkOwnership(ctx context.Context, paths []string, bookID int64) error {
	for _, p := range paths {
		owned, err := h.books.PathOwnedByOtherBook(ctx, p, bookID)
		if err != nil {
			return err
		}
		if owned {
			return refuse(http.StatusConflict, filepath.Base(p)+" already belongs to a book in your library.")
		}
	}
	return nil
}

// register writes the book_files rows and returns the paths this call
// inserted. On error the caller reverses what it returned.
func (h *AdoptionHandler) register(ctx context.Context, bookID int64, format string, paths, members []string) ([]string, error) {
	// Re-check against the resolved book: a concurrent import may have taken
	// a file in the moments since the first check.
	if err := h.checkOwnership(ctx, append(append([]string{}, paths...), members...), bookID); err != nil {
		return nil, err
	}
	var registered []string
	for _, p := range paths {
		inserted, err := h.registerFile(ctx, bookID, format, p)
		if err != nil {
			return registered, err
		}
		if inserted {
			registered = append(registered, p)
		}
	}
	return registered, nil
}

// reverse undoes a failed adopt's writes: the registered files, then a book
// and an author only if this request created them and nothing else holds
// them. Best effort; every step is logged.
func (h *AdoptionHandler) reverse(ctx context.Context, unitID int64, rec db.AdoptionRecord) {
	h.untrack(ctx, rec)
	h.removeCreated(ctx, unitID, rec)
}

// Undo handles POST /library/unmatched/{id}/undo. It untracks exactly the
// paths the adoption registered, then removes a book or author the adoption
// created, but only when the row recorded creating it, it has no other files
// or books, and no other row references it (S9).
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
	rec := db.AdoptionRecord{
		BookID: unit.BookID, CreatedBookID: unit.CreatedBookID,
		CreatedAuthorID: unit.CreatedAuthorID, RegisteredPaths: unit.RegisteredPaths,
	}
	h.untrack(work, rec)
	h.removeCreated(work, id, rec)
	if _, err := h.units.CompleteUndo(work, id); err != nil {
		writeServerError(w, r, err)
		return
	}
	slog.Info("adoption: undone", "unit", id, "book_id", rec.BookID, "files", len(rec.RegisteredPaths))
	h.writeUnit(w, r, id)
}

// untrack removes the adoption's book_files rows. A path that has since moved
// to a different book (a manual reassign, say) belongs to that book now and is
// left alone.
func (h *AdoptionHandler) untrack(ctx context.Context, rec db.AdoptionRecord) {
	for _, p := range rec.RegisteredPaths {
		if rec.BookID > 0 {
			if other, err := h.books.PathOwnedByOtherBook(ctx, p, rec.BookID); err != nil || other {
				continue
			}
		}
		if _, err := h.books.UntrackFilePath(ctx, p); err != nil {
			slog.Warn("adoption: could not untrack file", "path", p, "error", err)
		}
	}
}

// removeCreated deletes a book and an author the adoption created, each only
// when nothing else still depends on it.
func (h *AdoptionHandler) removeCreated(ctx context.Context, unitID int64, rec db.AdoptionRecord) {
	if rec.CreatedBookID > 0 && h.bookIsOnlyOurs(ctx, unitID, rec.CreatedBookID) {
		if err := h.books.Delete(ctx, rec.CreatedBookID); err != nil {
			slog.Warn("adoption: could not remove created book", "book_id", rec.CreatedBookID, "error", err)
		}
	}
	if rec.CreatedAuthorID > 0 {
		books, err := h.books.ListByAuthor(ctx, rec.CreatedAuthorID)
		if err != nil || len(books) > 0 {
			return
		}
		if other, err := h.units.AuthorReferencedElsewhere(ctx, rec.CreatedAuthorID, unitID); err != nil || other {
			return
		}
		if err := h.authors.Delete(ctx, rec.CreatedAuthorID); err != nil {
			slog.Warn("adoption: could not remove created author", "author_id", rec.CreatedAuthorID, "error", err)
		}
	}
}

func (h *AdoptionHandler) bookIsOnlyOurs(ctx context.Context, unitID, bookID int64) bool {
	files, err := h.books.ListFiles(ctx, bookID)
	if err != nil || len(files) > 0 {
		return false
	}
	other, err := h.units.BookReferencedElsewhere(ctx, bookID, unitID)
	return err == nil && !other
}
