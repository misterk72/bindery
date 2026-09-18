package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// existingBookSeriesLinkSampleLimit caps the titles carried in the run's
// summary log line, the same way the sync's skip samples are capped.
const existingBookSeriesLinkSampleLimit = 5

// existingBookSeriesLinker writes provider series refs onto books the library
// ALREADY has, during a catalogue sync.
//
// Until #2328 series membership was written in exactly one place,
// handleNewWantedBook, which runs only over the books a sync creates. Both
// branches that recognise an existing row return to the top of the loop before
// reaching it, so on an imported library, where every work resolves to a row
// that is already there and the author's monitoring refuses new ones, a
// refresh updated ratings, covers and genres and could never link a series.
// That left no way at all to repair series membership, which is most of what
// "relink this author to a better record and refresh" is for.
//
// Cost is the reason this is a type and not a function. It runs for every book
// of every author on every refresh, including the unattended discovery pass,
// so it holds the author's current memberships and resolved series ids across
// the whole sync:
//   - no provider call, ever. The refs come from the works fetch the sync has
//     already made, off the same models.Book the create path reads them from.
//   - one extra SELECT per sync, taken lazily on the first existing book that
//     carries a series ref. A provider that offers no series data, or an
//     author whose books are all new, issues nothing.
//   - writes only where a link is missing. A library already in the right
//     shape does the SELECT and stops.
//
// It is not safe for concurrent use; a catalogue sync holds the author's lock
// and walks its works serially.
type existingBookSeriesLinker struct {
	series   *db.SeriesRepo
	authorID int64

	loaded bool
	failed bool

	// linkedForeignIDs is book id → series foreign id → stored position.
	linkedForeignIDs map[int64]map[string]string
	// linkedTitles is book id → set of lowercased series titles the book is
	// already in, whatever id those rows carry. It is what stops the
	// cross-provider duplicate: the two providers mint series ids in
	// different namespaces, so "The Expanse" from Hardcover and "The
	// Expanse" from OpenLibrary are two rows in `series`, and linking by id
	// alone would file one book under both.
	linkedTitles map[int64]map[string]struct{}
	hasPrimary   map[int64]bool
	seriesIDs    map[string]int64

	linked         int
	conflicts      int
	conflictSample []string
}

func newExistingBookSeriesLinker(series *db.SeriesRepo, authorID int64) *existingBookSeriesLinker {
	return &existingBookSeriesLinker{series: series, authorID: authorID}
}

// link records the provider's series refs for a book that is already in the
// library. Best effort throughout: a series that cannot be upserted or a link
// that cannot be written is logged and skipped, never fatal to the sync, which
// is how the create path treats the same failures.
func (l *existingBookSeriesLinker) link(ctx context.Context, book *models.Book, refs []models.SeriesRef) {
	if l == nil || l.series == nil || book == nil || book.ID == 0 || len(refs) == 0 {
		return
	}
	if !l.load(ctx) {
		return
	}
	for _, ref := range refs {
		// The discovery job bounds each author with a context deadline, so a
		// long author stops here rather than running the rest of its works
		// against a cancelled context and logging a warning per ref.
		if ctx.Err() != nil {
			return
		}
		foreignID := strings.TrimSpace(ref.ForeignID)
		title := strings.TrimSpace(ref.Title)
		// CreateOrGet refuses an empty foreign id outright (#1645): without
		// one every caller would collapse onto a single shared row.
		if foreignID == "" || title == "" {
			continue
		}
		if have, ok := l.linkedForeignIDs[book.ID][foreignID]; ok {
			// The book is in this series already. Leave the stored row
			// exactly as it is, including a position that disagrees with the
			// provider: position is user editable, a renamer reads it, and a
			// refresh silently rewriting it would undo hand corrections with
			// no record. Counted and reported instead.
			if pos := strings.TrimSpace(ref.Position); pos != "" && pos != have {
				l.recordConflict(book, title, have, pos)
			}
			continue
		}
		// Same series under the other provider's id. Adding it would file one
		// volume under two series rows, with the renamer and the series page
		// each free to pick one. The stored row wins: it is the one the rest
		// of the library already points at.
		if _, sameTitle := l.linkedTitles[book.ID][strings.ToLower(title)]; sameTitle {
			slog.Debug("a series with this title is already linked under another provider id; leaving it",
				"book", book.Title, "bookId", book.ID, "series", title, "foreignId", foreignID)
			continue
		}
		seriesID, ok := l.resolveSeries(ctx, foreignID, title)
		if !ok {
			continue
		}
		// #2525: a book that is already filed under a primary series keeps
		// it. The create path can pass ref.Primary straight through because a
		// book it just made has no other membership; a stored book can, and
		// stamping a second primary_series row onto it leaves the renamer
		// choosing between them by query plan order.
		primary := ref.Primary && !l.hasPrimary[book.ID]
		created, err := l.series.LinkBookIfMissing(ctx, seriesID, book.ID, ref.Position, primary)
		if err != nil {
			slog.Warn("failed to link an existing book to its series", "book", book.Title, "series", title, "error", err)
			continue
		}
		l.remember(book.ID, foreignID, title, ref.Position, primary)
		if !created {
			// The row was there but not in the snapshot, which happens when
			// the book was re-parented onto this author during this same run.
			// INSERT OR IGNORE means nothing was duplicated; nothing to count.
			continue
		}
		l.linked++
		slog.Debug("linked an existing book to a series found on refresh",
			"book", book.Title, "bookId", book.ID, "series", title, "position", ref.Position, "primary", primary)
		// Mirrors the create path (#2245): a ref that names the series by a
		// Hardcover id is worth recording as a Hardcover link, or the series
		// shows "(no Hardcover link)" and Fill does nothing. Only on a link
		// this run actually created, so a settled library writes nothing.
		if linked, err := l.series.EnsureHardcoverLinkFromForeignID(ctx, seriesID, foreignID, title); err != nil {
			slog.Warn("failed to record hardcover series link", "series", title, "error", err)
		} else if linked {
			slog.Debug("linked series to hardcover from provider series ref", "series", title, "foreignId", foreignID)
		}
	}
}

// load takes the author's current memberships once per sync, on first use.
// Returns false when there is nothing to work from: a failed read must not be
// treated as "no memberships", or the linker would try to write a link for
// every book and rely on INSERT OR IGNORE to sort it out.
func (l *existingBookSeriesLinker) load(ctx context.Context) bool {
	if l.failed {
		return false
	}
	if l.loaded {
		return true
	}
	memberships, err := l.series.ListBookSeriesMembershipsByAuthor(ctx, l.authorID)
	if err != nil {
		slog.Warn("could not read existing series memberships; skipping series links for this refresh",
			"authorId", l.authorID, "error", err)
		l.failed = true
		return false
	}
	l.linkedForeignIDs = make(map[int64]map[string]string, len(memberships))
	l.linkedTitles = make(map[int64]map[string]struct{}, len(memberships))
	l.hasPrimary = make(map[int64]bool, len(memberships))
	l.seriesIDs = make(map[string]int64)
	for bookID, rows := range memberships {
		for _, m := range rows {
			if m.Primary {
				l.hasPrimary[bookID] = true
			}
			if title := strings.ToLower(strings.TrimSpace(m.SeriesTitle)); title != "" {
				if l.linkedTitles[bookID] == nil {
					l.linkedTitles[bookID] = make(map[string]struct{}, len(rows))
				}
				l.linkedTitles[bookID][title] = struct{}{}
			}
			if m.SeriesForeignID == "" {
				continue
			}
			if l.linkedForeignIDs[bookID] == nil {
				l.linkedForeignIDs[bookID] = make(map[string]string, len(rows))
			}
			l.linkedForeignIDs[bookID][m.SeriesForeignID] = strings.TrimSpace(m.Position)
			l.seriesIDs[m.SeriesForeignID] = m.SeriesID
		}
	}
	l.loaded = true
	return true
}

// resolveSeries maps a provider series foreign id to a local series row,
// creating it if this library has never seen it. Resolved ids are cached for
// the rest of the sync: an author's works commonly share a handful of series,
// and without the cache each one would re-run the upsert.
func (l *existingBookSeriesLinker) resolveSeries(ctx context.Context, foreignID, title string) (int64, bool) {
	if id, ok := l.seriesIDs[foreignID]; ok {
		return id, true
	}
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := l.series.CreateOrGet(ctx, s); err != nil {
		slog.Warn("failed to upsert series", "series", title, "error", err)
		return 0, false
	}
	l.seriesIDs[foreignID] = s.ID
	return s.ID, true
}

func (l *existingBookSeriesLinker) remember(bookID int64, foreignID, title, position string, primary bool) {
	if l.linkedForeignIDs[bookID] == nil {
		l.linkedForeignIDs[bookID] = make(map[string]string, 1)
	}
	l.linkedForeignIDs[bookID][foreignID] = strings.TrimSpace(position)
	if l.linkedTitles[bookID] == nil {
		l.linkedTitles[bookID] = make(map[string]struct{}, 1)
	}
	l.linkedTitles[bookID][strings.ToLower(title)] = struct{}{}
	if primary {
		l.hasPrimary[bookID] = true
	}
}

func (l *existingBookSeriesLinker) recordConflict(book *models.Book, series, stored, offered string) {
	l.conflicts++
	if len(l.conflictSample) < existingBookSeriesLinkSampleLimit {
		l.conflictSample = append(l.conflictSample, book.Title+" in "+series)
	}
	slog.Debug("keeping the stored position for a book already in this series",
		"book", book.Title, "bookId", book.ID, "series", series, "stored", stored, "provider", offered)
}

// logSummary reports the run's series work, and says nothing when there was
// none. A refresh over a settled library is the common case and it should not
// add a line to the log.
func (l *existingBookSeriesLinker) logSummary(authorName string) {
	if l == nil || (l.linked == 0 && l.conflicts == 0) {
		return
	}
	slog.Info("linked series onto books the library already had",
		"author", authorName, "linked", l.linked,
		"positionDisagreements", l.conflicts, "sample", l.conflictSample)
}
