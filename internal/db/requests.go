package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// RequestRepo stores requester requests (migration 089).
//
// Every method a requester's own traffic reaches takes the owner id and
// filters on it unconditionally, whether or not BINDERY_ENFORCE_TENANCY is on:
// one requester never sees, cancels or learns about another's requests. The
// admin methods (ListAll, CountPending, Claim, Complete, Release, Decline)
// are reached only through RequireAdmin routes.
type RequestRepo struct {
	db *sql.DB
}

func NewRequestRepo(db *sql.DB) *RequestRepo {
	return &RequestRepo{db: db}
}

var (
	// ErrRequestExists is a second request for the same kind and foreign id
	// by the same owner.
	ErrRequestExists = errors.New("request already exists")
	// ErrRequestNotPending is a state change on a request that is no longer
	// pending: already approved, declined, or claimed by another approval.
	ErrRequestNotPending = errors.New("request is not pending")
)

// RequestClaimTTL is how long an approval's claim holds before another
// approval may take the row over. An approval is a metadata lookup and a few
// inserts; a claim this old means the process died part way.
const RequestClaimTTL = 5 * time.Minute

const requestColumns = `r.id, r.owner_user_id, COALESCE(u.username, ''), r.kind, r.foreign_id, r.media_type,
	r.title, r.author_name, r.payload_json, r.status, r.decline_reason, r.decided_by,
	r.result_book_id, r.result_author_id, r.created_at, r.updated_at, r.decided_at,
	COALESCE(b.status, '')`

const requestJoins = `FROM requests r
	LEFT JOIN users u ON u.id = r.owner_user_id
	LEFT JOIN books b ON b.id = r.result_book_id`

func scanRequest(s scanner) (*models.LibraryRequest, error) {
	var (
		req                                 models.LibraryRequest
		decidedBy, resultBook, resultAuthor sql.NullInt64
		decidedAt                           sql.NullTime
	)
	if err := s.Scan(&req.ID, &req.OwnerUserID, &req.OwnerUsername, &req.Kind, &req.ForeignID, &req.MediaType,
		&req.Title, &req.AuthorName, &req.PayloadJSON, &req.Status, &req.DeclineReason, &decidedBy,
		&resultBook, &resultAuthor, &req.CreatedAt, &req.UpdatedAt, &decidedAt, &req.BookStatus); err != nil {
		return nil, err
	}
	if decidedBy.Valid {
		req.DecidedBy = &decidedBy.Int64
	}
	if resultBook.Valid {
		req.ResultBookID = &resultBook.Int64
	}
	if resultAuthor.Valid {
		req.ResultAuthorID = &resultAuthor.Int64
	}
	if decidedAt.Valid {
		t := decidedAt.Time
		req.DecidedAt = &t
	}
	return &req, nil
}

// Create inserts a pending request. A duplicate (owner, kind, foreign id)
// returns ErrRequestExists.
func (r *RequestRepo) Create(ctx context.Context, req *models.LibraryRequest) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO requests (owner_user_id, kind, foreign_id, media_type, title, author_name,
		                      payload_json, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		req.OwnerUserID, req.Kind, req.ForeignID, req.MediaType, req.Title, req.AuthorName,
		req.PayloadJSON, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrRequestExists
		}
		return fmt.Errorf("create request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("create request id: %w", err)
	}
	req.ID = id
	req.Status = models.RequestStatusPending
	req.CreatedAt, req.UpdatedAt = now, now
	return nil
}

// GetForOwner returns ownerID's request for kind and foreignID, or nil.
func (r *RequestRepo) GetForOwner(ctx context.Context, ownerID int64, kind, foreignID string) (*models.LibraryRequest, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` `+requestJoins+`
		WHERE r.owner_user_id = ? AND r.kind = ? AND r.foreign_id = ?`, ownerID, kind, foreignID)
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

// GetByID returns the request with id, or nil. Admin use only: it does not
// filter by owner.
func (r *RequestRepo) GetByID(ctx context.Context, id int64) (*models.LibraryRequest, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` `+requestJoins+` WHERE r.id = ?`, id)
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

// ListByOwner returns one page of ownerID's requests, newest first, and the
// owner's total.
func (r *RequestRepo) ListByOwner(ctx context.Context, ownerID int64, limit, offset int) ([]models.LibraryRequest, int, error) {
	return r.list(ctx, "WHERE r.owner_user_id = ?", []any{ownerID}, limit, offset)
}

// ListAll returns one page of every owner's requests, newest first. status
// narrows to one status; "pending" includes rows claimed by a running
// approval. Empty lists everything. Admin use only.
func (r *RequestRepo) ListAll(ctx context.Context, status string, limit, offset int) ([]models.LibraryRequest, int, error) {
	switch status {
	case "":
		return r.list(ctx, "", nil, limit, offset)
	case models.RequestStatusPending:
		return r.list(ctx, "WHERE r.status IN ('pending', 'approving')", nil, limit, offset)
	default:
		return r.list(ctx, "WHERE r.status = ?", []any{status}, limit, offset)
	}
}

func (r *RequestRepo) list(ctx context.Context, where string, args []any, limit, offset int) ([]models.LibraryRequest, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests r "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count requests: %w", err)
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.QueryContext(ctx, `SELECT `+requestColumns+` `+requestJoins+` `+where+`
		ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()
	out := []models.LibraryRequest{}
	var authorIDs []int64
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		if req.Kind == models.RequestKindAuthor && req.ResultAuthorID != nil {
			authorIDs = append(authorIDs, *req.ResultAuthorID)
		}
		out = append(out, *req)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.fillAuthorProgress(ctx, out, authorIDs); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// fillAuthorProgress sets AuthorBooks and AuthorBooksImported on the author
// requests in page with one grouped query for the whole page (plan item P6),
// never one query per row.
func (r *RequestRepo) fillAuthorProgress(ctx context.Context, page []models.LibraryRequest, authorIDs []int64) error {
	if len(authorIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(authorIDs)), ",")
	args := make([]any, len(authorIDs))
	for i, id := range authorIDs {
		args[i] = id
	}
	// #nosec G202 -- placeholders is a run of "?," built from a count, never input.
	rows, err := r.db.QueryContext(ctx, `
		SELECT author_id, COUNT(*), COALESCE(SUM(CASE WHEN status = 'imported' THEN 1 ELSE 0 END), 0)
		FROM books WHERE excluded = 0 AND author_id IN (`+placeholders+`)
		GROUP BY author_id`, args...)
	if err != nil {
		return fmt.Errorf("request author progress: %w", err)
	}
	defer rows.Close()
	type progress struct{ total, imported int }
	byAuthor := make(map[int64]progress, len(authorIDs))
	for rows.Next() {
		var id int64
		var p progress
		if err := rows.Scan(&id, &p.total, &p.imported); err != nil {
			return err
		}
		byAuthor[id] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range page {
		if page[i].Kind != models.RequestKindAuthor || page[i].ResultAuthorID == nil {
			continue
		}
		p := byAuthor[*page[i].ResultAuthorID]
		page[i].AuthorBooks, page[i].AuthorBooksImported = p.total, p.imported
	}
	return nil
}

// CountPendingByOwner counts ownerID's requests awaiting a decision.
func (r *RequestRepo) CountPendingByOwner(ctx context.Context, ownerID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE owner_user_id = ? AND status IN ('pending', 'approving')", ownerID).Scan(&n)
	return n, err
}

// CountPending counts every request awaiting a decision. Admin use only.
func (r *RequestRepo) CountPending(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE status IN ('pending', 'approving')").Scan(&n)
	return n, err
}

// Claim takes a pending request for approval by adminID. It is a compare and
// swap on status: of two approvals racing for one row, exactly one gets the
// row back and the other gets ErrRequestNotPending. A claim older than
// RequestClaimTTL is abandoned and can be retaken.
func (r *RequestRepo) Claim(ctx context.Context, id, adminID int64) (*models.LibraryRequest, error) {
	now := time.Now().UTC()
	stale := now.Add(-RequestClaimTTL).Unix()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'approving', claimed_at = ?, decided_by = ?, updated_at = ?
		WHERE id = ? AND (status = 'pending' OR (status = 'approving' AND COALESCE(claimed_at, 0) < ?))`,
		now.Unix(), nullableID(adminID), now, id, stale)
	if err != nil {
		return nil, fmt.Errorf("claim request: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrRequestNotPending
	}
	return r.GetByID(ctx, id)
}

// Complete marks a claimed request approved with what the approval created.
func (r *RequestRepo) Complete(ctx context.Context, id int64, bookID, authorID *int64) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'approved', result_book_id = ?, result_author_id = ?,
		       decided_at = ?, updated_at = ?, claimed_at = NULL
		WHERE id = ? AND status = 'approving'`, bookID, authorID, now, now, id)
	if err != nil {
		return fmt.Errorf("complete request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// Release returns a claimed request to pending after its approval failed.
func (r *RequestRepo) Release(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'pending', decided_by = NULL, claimed_at = NULL, updated_at = ?
		WHERE id = ? AND status = 'approving'`, time.Now().UTC(), id)
	return err
}

// Decline declines a pending request. Compare and swap on status, like Claim.
func (r *RequestRepo) Decline(ctx context.Context, id, adminID int64, reason string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'declined', decline_reason = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE id = ? AND status = 'pending'`, reason, nullableID(adminID), now, now, id)
	if err != nil {
		return fmt.Errorf("decline request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// Reopen puts ownerID's approved request back to pending, for when what it
// added has since left the library and the requester asks again. It keeps
// the row, so the (owner, kind, foreign id) uniqueness still holds.
func (r *RequestRepo) Reopen(ctx context.Context, id, ownerID int64, mediaType, payload string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'pending', media_type = ?, payload_json = ?, decided_by = NULL,
		       decided_at = NULL, result_book_id = NULL, result_author_id = NULL,
		       decline_reason = '', created_at = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND status = 'approved'`, mediaType, payload, now, now, id, ownerID)
	if err != nil {
		return fmt.Errorf("reopen request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// DeletePendingForOwner withdraws ownerID's own pending request. Returns
// ErrRequestNotPending when there is no such pending request for that owner,
// which covers another owner's id without saying it exists.
func (r *RequestRepo) DeletePendingForOwner(ctx context.Context, id, ownerID int64) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM requests WHERE id = ? AND owner_user_id = ? AND status = 'pending'", id, ownerID)
	if err != nil {
		return fmt.Errorf("withdraw request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
