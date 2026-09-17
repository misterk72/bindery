-- +migrate Up

-- Requests from the requester role: a person asks for a book or an author and
-- an admin approves or declines.
--
-- owner_user_id is the requester. It cascades on user delete: a request is
-- only meaningful to the person who made it, and nothing else points at it.
-- The books and authors an approval creates are owned by the requester through
-- the add pipeline, not through this table, so deleting a request never
-- deletes library data.
--
-- title and author_name are what the metadata provider said when the request
-- was made. The requester never supplies free text (security review S2).
-- payload_json is the server built add payload replayed at approval; it holds
-- the foreign ids and the media type and nothing the requester chose beyond
-- that.
--
-- status 'approving' is a claim held while an approval runs, so two admins
-- approving at once cannot both add. A claim older than a few minutes is
-- treated as abandoned (the process died mid approval) and can be retaken.
--
-- One row per owner, kind and foreign id: asking twice is a 409, not a second
-- row.
CREATE TABLE requests (
    id               INTEGER  PRIMARY KEY AUTOINCREMENT,
    owner_user_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             TEXT     NOT NULL CHECK (kind IN ('book', 'author')),
    foreign_id       TEXT     NOT NULL,
    media_type       TEXT     NOT NULL DEFAULT '',
    title            TEXT     NOT NULL DEFAULT '',
    author_name      TEXT     NOT NULL DEFAULT '',
    payload_json     TEXT     NOT NULL DEFAULT '{}',
    status           TEXT     NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'approving', 'approved', 'declined')),
    decline_reason   TEXT     NOT NULL DEFAULT '',
    decided_by       INTEGER  REFERENCES users(id) ON DELETE SET NULL,
    result_book_id   INTEGER  REFERENCES books(id) ON DELETE SET NULL,
    result_author_id INTEGER  REFERENCES authors(id) ON DELETE SET NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at       DATETIME,
    -- Unix seconds when an approval claimed the row; NULL unless approving.
    -- An integer so the stale claim test is a numeric comparison.
    claimed_at       INTEGER,
    UNIQUE (owner_user_id, kind, foreign_id)
);

-- The admin queue lists by status, newest first; the pending count reads the
-- same index.
CREATE INDEX idx_requests_status_created ON requests (status, created_at);

-- Off for every existing notification, so nothing new fires until an admin
-- turns it on (matches on_upgrade and on_health in 003).
ALTER TABLE notifications ADD COLUMN on_request_created INTEGER NOT NULL DEFAULT 0;
