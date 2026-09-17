package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
)

// UnmatchedListQuery filters and orders a page of unmatched units. Every
// field is bound or mapped through a switch; none reaches SQL text (S9).
type UnmatchedListQuery struct {
	// State is pending (default), ignored or adopted.
	State        string
	Reason       string
	AuthorFolder string
	Format       string
	Search       string
	// Sort is score (default), title, folder, files, size or seen.
	Sort string
	// Dir is asc or desc; empty picks the sort's natural direction.
	Dir    string
	Limit  int
	Offset int
}

// unmatchedListState maps a requested state to the stored state it lists.
// The in flight states (adopting, undoing) last for one request and are not
// listed, which keeps every list on a single state equality and so in index
// order (P4). A row a crashed request left claimed is released by the next
// scan's reconcile.
func unmatchedListState(state string) string {
	switch state {
	case UnmatchedStateIgnored, UnmatchedStateAdopted:
		return state
	default:
		return UnmatchedStatePending
	}
}

// unmatchedOrderBy maps a sort key and direction to a fixed ORDER BY clause.
func unmatchedOrderBy(sort, dir string) string {
	desc := func(natural bool) string {
		switch dir {
		case "asc":
			return "ASC"
		case "desc":
			return "DESC"
		}
		if natural {
			return "DESC"
		}
		return "ASC"
	}
	switch sort {
	case "title":
		return "parsed_title " + desc(false) + ", id"
	case "folder":
		return "author_folder " + desc(false) + ", rel_path " + desc(false)
	case "files":
		return "file_count " + desc(true) + ", rel_path"
	case "size":
		return "size_bytes " + desc(true) + ", rel_path"
	case "seen":
		return "first_seen_at " + desc(true) + ", id"
	default:
		return "top_score " + desc(true) + ", rel_path"
	}
}

func unmatchedWhere(q UnmatchedListQuery, includeFacetFilters bool) (string, []any) {
	where := "state = ?"
	args := []any{unmatchedListState(q.State)}
	if includeFacetFilters {
		if q.Reason != "" {
			where += " AND reason = ?"
			args = append(args, q.Reason)
		}
		if q.AuthorFolder != "" {
			where += " AND author_folder = ?"
			args = append(args, q.AuthorFolder)
		}
		if q.Format != "" {
			where += " AND format = ?"
			args = append(args, q.Format)
		}
	}
	// A substring match over at most 20,000 rows (the scan's unit cap), one
	// LIKE per folded word. FoldForSearch already drops LIKE's wildcards; the
	// escape stays so that never depends on the fold. Acceptable at that size, and
	// every other predicate above is index backed (P4).
	if folded := textutil.FoldForSearch(q.Search); folded != "" {
		for _, tok := range searchTokens(folded) {
			where += " AND search_key LIKE ? ESCAPE '\\'"
			args = append(args, "%"+escapeLike(tok)+"%")
		}
	}
	return where, args
}

// List returns one page of units and the total matching the filters.
func (r *UnmatchedUnitRepo) List(ctx context.Context, q UnmatchedListQuery) ([]UnmatchedUnit, int, error) {
	where, args := unmatchedWhere(q, true)
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM unmatched_units WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("unmatched units: count: %w", err)
	}
	limit := q.Limit
	if limit <= 0 || limit > 250 {
		limit = 50
	}
	offset := max(q.Offset, 0)
	//nolint:gosec // G202: where is fixed predicates with bound values (unmatchedWhere) and the ORDER BY comes from the unmatchedOrderBy switch
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+unmatchedUnitColumns+` FROM unmatched_units WHERE `+where+
			` ORDER BY `+unmatchedOrderBy(q.Sort, q.Dir)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("unmatched units: list: %w", err)
	}
	defer rows.Close()
	out := make([]UnmatchedUnit, 0, limit)
	for rows.Next() {
		u, err := scanUnmatchedUnit(rows.Scan)
		if err != nil {
			return nil, 0, fmt.Errorf("unmatched units: list scan: %w", err)
		}
		out = append(out, *u)
	}
	return out, total, rows.Err()
}

// FacetCount is one value of a facet and how many units carry it.
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// FolderFacet is one author folder in the structure rail.
type FolderFacet struct {
	Folder string `json:"folder"`
	Units  int    `json:"units"`
	Files  int    `json:"files"`
	// NotInLibrary counts units whose author matched no library author. When
	// it equals Units the whole folder is one decision: add the author.
	NotInLibrary int `json:"notInLibrary"`
	// Author is the parsed author most units in the folder share.
	Author string `json:"author"`
}

// UnmatchedFacets are the grouped counts behind the filters and the rail.
type UnmatchedFacets struct {
	Reasons []FacetCount  `json:"reasons"`
	Formats []FacetCount  `json:"formats"`
	Folders []FolderFacet `json:"folders"`
}

// maxFolderFacets bounds the rail. The rail is for the big decisions; a long
// tail of one book folders is what the table and its folder filter are for.
const maxFolderFacets = 12

// Facets runs the three grouped counts for a state and search. The web asks
// for them only when that pair changes, not on every page turn (P4).
func (r *UnmatchedUnitRepo) Facets(ctx context.Context, q UnmatchedListQuery) (UnmatchedFacets, error) {
	where, args := unmatchedWhere(q, false)
	f := UnmatchedFacets{Reasons: []FacetCount{}, Formats: []FacetCount{}, Folders: []FolderFacet{}}
	grouped := func(col string) ([]FacetCount, error) {
		//nolint:gosec // G202: col is one of two literal column names below; where binds every value
		rows, err := r.db.QueryContext(ctx,
			`SELECT `+col+`, COUNT(*) FROM unmatched_units WHERE `+where+` GROUP BY `+col+` ORDER BY COUNT(*) DESC, `+col, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []FacetCount{}
		for rows.Next() {
			var fc FacetCount
			if err := rows.Scan(&fc.Value, &fc.Count); err != nil {
				return nil, err
			}
			out = append(out, fc)
		}
		return out, rows.Err()
	}
	var err error
	if f.Reasons, err = grouped("reason"); err != nil {
		return f, fmt.Errorf("unmatched units: reason facet: %w", err)
	}
	if f.Formats, err = grouped("format"); err != nil {
		return f, fmt.Errorf("unmatched units: format facet: %w", err)
	}
	// The most common parsed author per folder comes from a correlated
	// subquery that only runs for the handful of folders the LIMIT keeps. Its
	// unqualified columns resolve to the innermost table, a.
	//nolint:gosec // G202: where is fixed predicates with bound values (unmatchedWhere)
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.author_folder, g.units, g.files, g.not_in_library,
		       COALESCE((SELECT parsed_author FROM unmatched_units a
		                 WHERE a.author_folder = g.author_folder AND a.parsed_author <> '' AND `+where+`
		                 GROUP BY parsed_author ORDER BY COUNT(*) DESC LIMIT 1), '')
		FROM (
		    SELECT author_folder, COUNT(*) AS units, SUM(file_count) AS files,
		           SUM(CASE WHEN reason = 'author_not_in_library' THEN 1 ELSE 0 END) AS not_in_library
		    FROM unmatched_units WHERE author_folder <> '' AND `+where+`
		    GROUP BY author_folder ORDER BY units DESC, author_folder LIMIT ?
		) g`, append(append(append([]any{}, args...), args...), maxFolderFacets)...)
	if err != nil {
		return f, fmt.Errorf("unmatched units: folder facet: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ff FolderFacet
		if err := rows.Scan(&ff.Folder, &ff.Units, &ff.Files, &ff.NotInLibrary, &ff.Author); err != nil {
			return f, fmt.Errorf("unmatched units: folder facet scan: %w", err)
		}
		f.Folders = append(f.Folders, ff)
	}
	return f, rows.Err()
}

// UnmatchedBookRef is the slice of a book the adoption list shows beside a
// suggestion or an adopted row.
type UnmatchedBookRef struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	AuthorID   int64  `json:"authorId"`
	AuthorName string `json:"authorName"`
	ImageURL   string `json:"imageUrl,omitempty"`
	Status     string `json:"status"`
	MediaType  string `json:"mediaType"`
	Monitored  bool   `json:"monitored"`
}

// BookRefs loads the books a page of units points at in one IN query (P4,
// T5), whatever the number of rows and candidates on the page.
func (r *UnmatchedUnitRepo) BookRefs(ctx context.Context, ids []int64) (map[int64]UnmatchedBookRef, error) {
	out := make(map[int64]UnmatchedBookRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	//nolint:gosec // G202: the IN list is generated ? placeholders; every id is bound
	rows, err := r.db.QueryContext(ctx, `
		SELECT b.id, b.title, b.author_id, COALESCE(a.name, ''), COALESCE(b.image_url, ''),
		       b.status, b.media_type, b.monitored
		FROM books b LEFT JOIN authors a ON a.id = b.author_id
		WHERE b.id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("unmatched units: book refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var b UnmatchedBookRef
		var authorID sql.NullInt64
		if err := rows.Scan(&b.ID, &b.Title, &authorID, &b.AuthorName, &b.ImageURL, &b.Status, &b.MediaType, &b.Monitored); err != nil {
			return nil, fmt.Errorf("unmatched units: book refs scan: %w", err)
		}
		b.AuthorID = authorID.Int64
		out[b.ID] = b
	}
	return out, rows.Err()
}
