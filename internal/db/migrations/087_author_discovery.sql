-- +migrate Up
-- Unattended release discovery (#2236).
--
-- authors.last_discovery_at is the cursor the scheduled discovery job walks.
-- It gets its own column because last_metadata_refresh_at is only stamped when
-- a profile field actually changed, so it cannot say when an author's
-- catalogue was last checked. NULL means never checked, and the job visits
-- those authors first. Written by a single column UPDATE, never by the whole
-- row author update, so a user edit racing a discovery pass loses nothing.
--
-- notifications.on_book_announced opts a webhook into the bookAnnounced event.
-- It defaults to 0 for existing and new rows alike, the same as on_upgrade and
-- on_health, so nothing new fires until an admin turns it on.
-- NOTE: no semicolons inside comments, the migration runner splits on them.
ALTER TABLE authors ADD COLUMN last_discovery_at DATETIME;

ALTER TABLE notifications ADD COLUMN on_book_announced INTEGER NOT NULL DEFAULT 0;
