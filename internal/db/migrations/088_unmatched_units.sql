-- +migrate Up

-- Library adoption: the files a library scan could not match, one row per
-- book rather than per file.
--
-- Until now the scan wrote its unmatched files into the library.lastScan
-- settings blob, capped at 1000 entries, one entry per file. A row there had
-- no identity, so nothing could be decided about it: no ignore state, no
-- grouping of a 193 track audiobook into one book (#2547), and no way to mark
-- a file as dealt with. This table gives each unit a stable identity keyed by
-- its path, so a decision (adopted, ignored) survives the next scan.
--
-- state:
--   pending   the scan still finds this unit and nobody has decided yet
--   adopting  an adopt request holds the row (compare and swap claim)
--   adopted   files registered against book_id by an admin
--   undoing   an undo request holds the row
--   ignored   an admin said this unit is not a book to track
--
-- created_book_id and created_author_id are set only when the adopt request
-- itself inserted that row, which is what lets Undo remove them again without
-- touching a book or author that existed before. registered_paths_json lists
-- exactly the book_files paths the adopt inserted, for the same reason.
--
-- member_paths_json holds the files that make up the unit. Adopt re-checks
-- each one on disk before registering anything, because the row may be hours
-- old by then.
--
-- Times are written by the repository in one fixed width UTC layout so the
-- purge and in flight recovery comparisons below can compare them as text.

CREATE TABLE unmatched_units (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    unit_path             TEXT     NOT NULL UNIQUE,
    unit_kind             TEXT     NOT NULL CHECK(unit_kind IN ('file', 'folder')),
    format                TEXT     NOT NULL CHECK(format IN ('ebook', 'audiobook')),
    file_count            INTEGER  NOT NULL DEFAULT 1,
    size_bytes            INTEGER  NOT NULL DEFAULT 0,
    root_path             TEXT     NOT NULL DEFAULT '',
    rel_path              TEXT     NOT NULL DEFAULT '',
    author_folder         TEXT     NOT NULL DEFAULT '',
    parsed_title          TEXT     NOT NULL DEFAULT '',
    parsed_author         TEXT     NOT NULL DEFAULT '',
    reason                TEXT     NOT NULL DEFAULT '',
    search_key            TEXT     NOT NULL DEFAULT '',
    member_paths_json     TEXT     NOT NULL DEFAULT '[]',
    candidates_json       TEXT     NOT NULL DEFAULT '[]',
    top_score             REAL     NOT NULL DEFAULT 0,
    state                 TEXT     NOT NULL DEFAULT 'pending'
                                   CHECK(state IN ('pending', 'adopting', 'adopted', 'undoing', 'ignored')),
    book_id               INTEGER  REFERENCES books(id) ON DELETE SET NULL,
    created_book_id       INTEGER  REFERENCES books(id) ON DELETE SET NULL,
    created_author_id     INTEGER  REFERENCES authors(id) ON DELETE SET NULL,
    registered_paths_json TEXT     NOT NULL DEFAULT '[]',
    scan_generation       INTEGER  NOT NULL DEFAULT 0,
    first_seen_at         TEXT     NOT NULL,
    last_seen_at          TEXT     NOT NULL,
    resolved_at           TEXT,
    updated_at            TEXT     NOT NULL
);

-- The default list: pending units, best suggestion first.
CREATE INDEX idx_unmatched_units_state_score ON unmatched_units(state, top_score DESC, rel_path);
-- The folder rail and the folder filter.
CREATE INDEX idx_unmatched_units_state_folder ON unmatched_units(state, author_folder, rel_path);
-- The reason facet and filter.
CREATE INDEX idx_unmatched_units_state_reason ON unmatched_units(state, reason);
-- Undo's "does any other row still point at this book" check.
CREATE INDEX idx_unmatched_units_book ON unmatched_units(book_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_unmatched_units_book;
DROP INDEX IF EXISTS idx_unmatched_units_state_reason;
DROP INDEX IF EXISTS idx_unmatched_units_state_folder;
DROP INDEX IF EXISTS idx_unmatched_units_state_score;
DROP TABLE IF EXISTS unmatched_units;
