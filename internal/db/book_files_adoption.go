package db

import (
	"context"
	"fmt"
)

// UntrackFilePathForBook removes the book_files row for path only while it
// still belongs to bookID, then refreshes that book's status. It reports
// whether a row was removed. The file on disk is never touched.
//
// Library adoption's Undo uses it instead of UntrackFilePath: between the adopt
// and the undo the path may have been deleted with its book and registered to
// another one, and that book's row must survive the undo.
func (r *BookRepo) UntrackFilePathForBook(ctx context.Context, path string, bookID int64) (bool, error) {
	out, err := r.exec.ExecContext(ctx, `DELETE FROM book_files WHERE path = ? AND book_id = ?`, path, bookID)
	if err != nil {
		return false, fmt.Errorf("untrack file path for book: %w", err)
	}
	n, err := out.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	return true, r.refreshBookStatus(ctx, bookID)
}
