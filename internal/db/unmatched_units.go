package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/textutil"
)

// Unmatched unit states. See migration 088 for what each one means.
const (
	UnmatchedStatePending  = "pending"
	UnmatchedStateAdopting = "adopting"
	UnmatchedStateAdopted  = "adopted"
	UnmatchedStateUndoing  = "undoing"
	UnmatchedStateIgnored  = "ignored"
)

// Unit kinds: a single file (an ebook, or loose audio at a library root), or
// a folder that stands for one book (an audiobook folder or a disc set).
const (
	UnmatchedKindFile   = "file"
	UnmatchedKindFolder = "folder"
)

const (
	// reconcileChunkSize bounds how long one scan tail holds SQLite's single
	// writer lock (P3). Imports and the queue get the lock between chunks.
	reconcileChunkSize = 500
	// unmatchedPurgeAge is how long an ignored or adopted row may go unseen by
	// a scan before it is purged.
	unmatchedPurgeAge = 30 * 24 * time.Hour
	// unmatchedInFlightTimeout recovers a row a crashed adopt or undo left
	// claimed. No real request holds a row anywhere near this long.
	unmatchedInFlightTimeout = 15 * time.Minute
)

// unitTimeLayout is fixed width, so two stored times compare correctly as
// text. RFC3339Nano trims trailing zeros, and "05Z" sorts after "05.1Z".
const unitTimeLayout = "2006-01-02T15:04:05.000000000Z"

func unitTime(t time.Time) string { return t.UTC().Format(unitTimeLayout) }

// UnmatchedCandidate is one catalogue book the scan thought this unit might
// be, with the title similarity that put it there.
type UnmatchedCandidate struct {
	BookID int64   `json:"bookId"`
	Score  float64 `json:"score"`
}

// UnmatchedUnitScan is what a library scan reports for one unit.
type UnmatchedUnitScan struct {
	UnitPath     string
	UnitKind     string
	Format       string
	FileCount    int
	SizeBytes    int64
	RootPath     string
	RelPath      string
	AuthorFolder string
	ParsedTitle  string
	ParsedAuthor string
	Reason       string
	MemberPaths  []string
	Candidates   []UnmatchedCandidate
}

// UnmatchedUnit is one stored row.
type UnmatchedUnit struct {
	ID              int64
	UnitPath        string
	UnitKind        string
	Format          string
	FileCount       int
	SizeBytes       int64
	RootPath        string
	RelPath         string
	AuthorFolder    string
	ParsedTitle     string
	ParsedAuthor    string
	Reason          string
	MemberPaths     []string
	Candidates      []UnmatchedCandidate
	TopScore        float64
	State           string
	BookID          int64
	CreatedBookID   int64
	CreatedAuthorID int64
	RegisteredPaths []string
	ScanGeneration  int64
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	ResolvedAt      *time.Time
}

// ReconcileScanOptions tunes one ReconcileScan call.
type ReconcileScanOptions struct {
	// StartedAt is when the scan read the tracked paths. An adopted row the
	// scan still found unmatched returns to pending only when it was adopted
	// before this, so an adopt that lands while a long walk is running is not
	// undone by that walk's stale view.
	StartedAt time.Time
	// SkipDeletion keeps every existing row. Set when the scan found no files
	// or failed, so an unmounted volume cannot erase anyone's decisions.
	SkipDeletion bool
	// SkipPurge keeps ignored and adopted rows past their age. Set when the
	// scan was truncated, because a unit beyond the cap was not unseen.
	SkipPurge bool
	// Now is the clock; zero means time.Now.
	Now time.Time
}

// ReconcileScanResult reports what a reconcile did and the counts after it.
type ReconcileScanResult struct {
	Generation     int64
	Upserted       int
	RemovedPending int64
	Purged         int64
	Pending        int
	Ignored        int
}

// UnmatchedUnitRepo stores library adoption rows.
type UnmatchedUnitRepo struct {
	db *sql.DB
}

// NewUnmatchedUnitRepo returns a repo over database.
func NewUnmatchedUnitRepo(database *sql.DB) *UnmatchedUnitRepo {
	return &UnmatchedUnitRepo{db: database}
}

const upsertUnmatchedUnitSQL = `
INSERT INTO unmatched_units (
    unit_path, unit_kind, format, file_count, size_bytes, root_path, rel_path,
    author_folder, parsed_title, parsed_author, reason, search_key,
    member_paths_json, candidates_json, top_score, scan_generation,
    first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(unit_path) DO UPDATE SET
    unit_kind = excluded.unit_kind,
    format = excluded.format,
    file_count = excluded.file_count,
    size_bytes = excluded.size_bytes,
    root_path = excluded.root_path,
    rel_path = excluded.rel_path,
    author_folder = excluded.author_folder,
    parsed_title = excluded.parsed_title,
    parsed_author = excluded.parsed_author,
    reason = excluded.reason,
    search_key = excluded.search_key,
    member_paths_json = excluded.member_paths_json,
    candidates_json = excluded.candidates_json,
    top_score = excluded.top_score,
    scan_generation = excluded.scan_generation,
    last_seen_at = excluded.last_seen_at,
    updated_at = excluded.updated_at,
    -- An adoption whose files the scan no longer sees as tracked has been
    -- undone some other way (the book was deleted, a file row was removed),
    -- so the unit is a decision to make again. Ignored stays ignored, and a
    -- row adopted after this scan started is left alone.
    state = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                 THEN 'pending' ELSE unmatched_units.state END,
    book_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                   THEN NULL ELSE unmatched_units.book_id END,
    created_book_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                           THEN NULL ELSE unmatched_units.created_book_id END,
    created_author_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                             THEN NULL ELSE unmatched_units.created_author_id END,
    registered_paths_json = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                                 THEN '[]' ELSE unmatched_units.registered_paths_json END,
    resolved_at = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                       THEN NULL ELSE unmatched_units.resolved_at END`

// ReconcileScan records one scan's units. Upserts are keyed by unit_path and
// committed every reconcileChunkSize rows (P3). A decision already taken on a
// row is kept: the upsert never moves an ignored row, and moves an adopted row
// back to pending only as described in upsertUnmatchedUnitSQL. Rows a crashed
// request left claimed are released first. After the upserts, one short
// transaction removes pending rows this scan did not see and purges ignored
// and adopted rows unseen for 30 days. The generation number keeps this correct if
// the process dies between chunks: the next scan's generation supersedes it.
func (r *UnmatchedUnitRepo) ReconcileScan(ctx context.Context, units []UnmatchedUnitScan, opts ReconcileScanOptions) (ReconcileScanResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	started := opts.StartedAt
	if started.IsZero() {
		started = now
	}
	var res ReconcileScanResult
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(scan_generation), 0) + 1 FROM unmatched_units`).Scan(&res.Generation); err != nil {
		return res, fmt.Errorf("unmatched units: next generation: %w", err)
	}
	nowText := unitTime(now)
	startedText := unitTime(started)

	// Release rows a crashed adopt or undo left claimed. First, because the
	// upserts below refresh updated_at, which is what dates a claim.
	if _, err := r.db.ExecContext(ctx,
		`UPDATE unmatched_units SET state = CASE state WHEN 'adopting' THEN 'pending' ELSE 'adopted' END, updated_at = ?
		 WHERE state IN ('adopting', 'undoing') AND updated_at < ?`,
		nowText, unitTime(now.Add(-unmatchedInFlightTimeout))); err != nil {
		return res, fmt.Errorf("unmatched units: release stale claims: %w", err)
	}

	for start := 0; start < len(units); start += reconcileChunkSize {
		end := min(start+reconcileChunkSize, len(units))
		if err := r.upsertChunk(ctx, units[start:end], res.Generation, nowText, startedText); err != nil {
			return res, err
		}
		res.Upserted += end - start
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("unmatched units: begin cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !opts.SkipDeletion {
		out, err := tx.ExecContext(ctx,
			`DELETE FROM unmatched_units WHERE state = 'pending' AND scan_generation < ?`, res.Generation)
		if err != nil {
			return res, fmt.Errorf("unmatched units: remove unseen pending: %w", err)
		}
		res.RemovedPending, _ = out.RowsAffected()
		if !opts.SkipPurge {
			out, err := tx.ExecContext(ctx,
				`DELETE FROM unmatched_units WHERE state IN ('ignored', 'adopted') AND last_seen_at < ?`,
				unitTime(now.Add(-unmatchedPurgeAge)))
			if err != nil {
				return res, fmt.Errorf("unmatched units: purge old decisions: %w", err)
			}
			res.Purged, _ = out.RowsAffected()
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("unmatched units: commit cleanup: %w", err)
	}

	summary, err := r.Summary(ctx)
	if err != nil {
		return res, err
	}
	res.Pending = summary.Pending
	res.Ignored = summary.Ignored
	return res, nil
}

func (r *UnmatchedUnitRepo) upsertChunk(ctx context.Context, chunk []UnmatchedUnitScan, generation int64, nowText, startedText string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unmatched units: begin chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, upsertUnmatchedUnitSQL)
	if err != nil {
		return fmt.Errorf("unmatched units: prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range chunk {
		u := &chunk[i]
		members, err := json.Marshal(nonNilStrings(u.MemberPaths))
		if err != nil {
			return fmt.Errorf("unmatched units: encode members: %w", err)
		}
		cands := u.Candidates
		if cands == nil {
			cands = []UnmatchedCandidate{}
		}
		candJSON, err := json.Marshal(cands)
		if err != nil {
			return fmt.Errorf("unmatched units: encode candidates: %w", err)
		}
		var top float64
		for _, c := range cands {
			top = max(top, c.Score)
		}
		fileCount := max(u.FileCount, 1)
		searchKey := textutil.FoldForSearch(u.ParsedTitle + " " + u.ParsedAuthor + " " + u.RelPath)
		if _, err := stmt.ExecContext(ctx,
			u.UnitPath, u.UnitKind, u.Format, fileCount, u.SizeBytes, u.RootPath, u.RelPath,
			u.AuthorFolder, u.ParsedTitle, u.ParsedAuthor, u.Reason, searchKey,
			string(members), string(candJSON), top, generation,
			nowText, nowText, nowText,
			startedText, startedText, startedText, startedText, startedText, startedText,
		); err != nil {
			return fmt.Errorf("unmatched units: upsert %q: %w", u.UnitPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("unmatched units: commit chunk: %w", err)
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// UnmatchedSummary is the headline counts, cheap enough for a nav badge.
type UnmatchedSummary struct {
	Pending      int `json:"pending"`
	PendingFiles int `json:"pendingFiles"`
	Ignored      int `json:"ignored"`
	Adopted      int `json:"adopted"`
}

// Summary counts rows by state. The adopting and undoing states count with
// the state they came from, since that is what the row still is to a viewer.
func (r *UnmatchedUnitRepo) Summary(ctx context.Context) (UnmatchedSummary, error) {
	var s UnmatchedSummary
	rows, err := r.db.QueryContext(ctx,
		`SELECT state, COUNT(*), COALESCE(SUM(file_count), 0) FROM unmatched_units GROUP BY state`)
	if err != nil {
		return s, fmt.Errorf("unmatched units: summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, files int
		if err := rows.Scan(&state, &n, &files); err != nil {
			return s, fmt.Errorf("unmatched units: summary scan: %w", err)
		}
		switch state {
		case UnmatchedStatePending, UnmatchedStateAdopting:
			s.Pending += n
			s.PendingFiles += files
		case UnmatchedStateAdopted, UnmatchedStateUndoing:
			s.Adopted += n
		case UnmatchedStateIgnored:
			s.Ignored += n
		}
	}
	return s, rows.Err()
}

const unmatchedUnitColumns = `id, unit_path, unit_kind, format, file_count, size_bytes, root_path, rel_path,
    author_folder, parsed_title, parsed_author, reason, member_paths_json, candidates_json, top_score,
    state, book_id, created_book_id, created_author_id, registered_paths_json, scan_generation,
    first_seen_at, last_seen_at, resolved_at`

func scanUnmatchedUnit(scan func(...any) error) (*UnmatchedUnit, error) {
	var u UnmatchedUnit
	var members, cands, registered string
	var bookID, createdBook, createdAuthor sql.NullInt64
	var first, last, resolved sql.NullString
	if err := scan(&u.ID, &u.UnitPath, &u.UnitKind, &u.Format, &u.FileCount, &u.SizeBytes, &u.RootPath, &u.RelPath,
		&u.AuthorFolder, &u.ParsedTitle, &u.ParsedAuthor, &u.Reason, &members, &cands, &u.TopScore,
		&u.State, &bookID, &createdBook, &createdAuthor, &registered, &u.ScanGeneration,
		&first, &last, &resolved); err != nil {
		return nil, err
	}
	u.BookID, u.CreatedBookID, u.CreatedAuthorID = bookID.Int64, createdBook.Int64, createdAuthor.Int64
	if err := json.Unmarshal([]byte(members), &u.MemberPaths); err != nil {
		return nil, fmt.Errorf("unmatched unit %d members: %w", u.ID, err)
	}
	if err := json.Unmarshal([]byte(cands), &u.Candidates); err != nil {
		return nil, fmt.Errorf("unmatched unit %d candidates: %w", u.ID, err)
	}
	if err := json.Unmarshal([]byte(registered), &u.RegisteredPaths); err != nil {
		return nil, fmt.Errorf("unmatched unit %d registered paths: %w", u.ID, err)
	}
	u.FirstSeenAt = parseFlexibleTimeValue(first, "unmatched_units.first_seen_at")
	u.LastSeenAt = parseFlexibleTimeValue(last, "unmatched_units.last_seen_at")
	if t, err := parseFlexibleTime(resolved); err == nil {
		u.ResolvedAt = t
	}
	return &u, nil
}

// Get returns one row, or nil when there is none.
func (r *UnmatchedUnitRepo) Get(ctx context.Context, id int64) (*UnmatchedUnit, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+unmatchedUnitColumns+` FROM unmatched_units WHERE id = ?`, id)
	u, err := scanUnmatchedUnit(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("unmatched units: get %d: %w", id, err)
	}
	return u, nil
}

// ClaimState moves a row from one state to another only if it is still in
// the first. It is the compare and swap every adopt, undo and ignore goes
// through (S9): a single UPDATE is atomic in SQLite, so of two requests racing
// for one row exactly one sees a row affected.
func (r *UnmatchedUnitRepo) ClaimState(ctx context.Context, id int64, from, to string) (bool, error) {
	out, err := r.db.ExecContext(ctx,
		`UPDATE unmatched_units SET state = ?, updated_at = ? WHERE id = ? AND state = ?`,
		to, unitTime(time.Now()), id, from)
	if err != nil {
		return false, fmt.Errorf("unmatched units: claim %d %s to %s: %w", id, from, to, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// AdoptionRecord is what a finished adopt wrote, and what undo reverses.
type AdoptionRecord struct {
	BookID          int64
	CreatedBookID   int64
	CreatedAuthorID int64
	RegisteredPaths []string
}

// CompleteAdoption moves a claimed row to adopted and records what the adopt
// did. It reports false when the row is no longer held by the adopt.
func (r *UnmatchedUnitRepo) CompleteAdoption(ctx context.Context, id int64, rec AdoptionRecord) (bool, error) {
	paths, err := json.Marshal(nonNilStrings(rec.RegisteredPaths))
	if err != nil {
		return false, err
	}
	now := unitTime(time.Now())
	out, err := r.db.ExecContext(ctx, `
		UPDATE unmatched_units
		SET state = 'adopted', book_id = ?, created_book_id = ?, created_author_id = ?,
		    registered_paths_json = ?, resolved_at = ?, updated_at = ?
		WHERE id = ? AND state = 'adopting'`,
		nullID(rec.BookID), nullID(rec.CreatedBookID), nullID(rec.CreatedAuthorID), string(paths), now, now, id)
	if err != nil {
		return false, fmt.Errorf("unmatched units: complete adoption %d: %w", id, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// CompleteUndo returns a claimed row to pending and clears the adoption.
func (r *UnmatchedUnitRepo) CompleteUndo(ctx context.Context, id int64) (bool, error) {
	out, err := r.db.ExecContext(ctx, `
		UPDATE unmatched_units
		SET state = 'pending', book_id = NULL, created_book_id = NULL, created_author_id = NULL,
		    registered_paths_json = '[]', resolved_at = NULL, updated_at = ?
		WHERE id = ? AND state = 'undoing'`, unitTime(time.Now()), id)
	if err != nil {
		return false, fmt.Errorf("unmatched units: complete undo %d: %w", id, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// IgnorePending marks pending rows ignored, selected by id or by author
// folder (exactly one of the two). Rows in any other state are left alone.
func (r *UnmatchedUnitRepo) IgnorePending(ctx context.Context, ids []int64, authorFolder string) (int64, error) {
	now := unitTime(time.Now())
	var out sql.Result
	var err error
	switch {
	case len(ids) > 0:
		ph := make([]string, len(ids))
		args := make([]any, 0, len(ids)+1)
		args = append(args, now)
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		//nolint:gosec // G202: the IN list is generated ? placeholders; every id is bound
		out, err = r.db.ExecContext(ctx,
			`UPDATE unmatched_units SET state = 'ignored', updated_at = ? WHERE state = 'pending' AND id IN (`+strings.Join(ph, ",")+`)`,
			args...)
	case authorFolder != "":
		out, err = r.db.ExecContext(ctx,
			`UPDATE unmatched_units SET state = 'ignored', updated_at = ? WHERE state = 'pending' AND author_folder = ?`,
			now, authorFolder)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("unmatched units: ignore: %w", err)
	}
	return out.RowsAffected()
}

// BookReferencedElsewhere reports whether any row other than exceptID still
// points at bookID, as its adopted book or as a book it created.
func (r *UnmatchedUnitRepo) BookReferencedElsewhere(ctx context.Context, bookID, exceptID int64) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM unmatched_units WHERE id <> ? AND (book_id = ? OR created_book_id = ?) LIMIT 1`,
		exceptID, bookID, bookID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AuthorReferencedElsewhere is BookReferencedElsewhere for a created author.
func (r *UnmatchedUnitRepo) AuthorReferencedElsewhere(ctx context.Context, authorID, exceptID int64) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM unmatched_units WHERE id <> ? AND created_author_id = ? LIMIT 1`,
		exceptID, authorID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
