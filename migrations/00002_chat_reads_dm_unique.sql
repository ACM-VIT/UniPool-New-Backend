-- +goose NO TRANSACTION
-- +goose Up

-- chat_reads needed a UNIQUE index on (user_id, dm_room_id) to back
-- the ON CONFLICT clause in routes/chat/chat.go::MarkDMRead. The
-- baseline schema mistakenly defined idx_chat_reads_user_dm as
-- UNIQUE on (dm_room_id) alone, which means:
--
--   (a) ON CONFLICT (user_id, dm_room_id) doesn't match any
--       constraint and Postgres errors out (SQLSTATE 42P10) — DM
--       read-marks were silently 500'ing on any deployment that
--       didn't have a hand-applied partial index.
--   (b) The wrong-shape unique would have prevented two users
--       from ever creating chat_read rows for the same DM room
--       (alice's read-mark of dm_a_b would block bob's read-mark
--       of dm_a_b), which is the opposite of the read-cursor
--       semantics the handler wants.
--
-- Create the right-shape index under a new name first (so prod
-- traffic keeps working against the old constraint while the new
-- one builds), THEN drop the wrong-shape one. Splitting into
-- separate statements via NO TRANSACTION lets CockroachDB schedule
-- the drop asynchronously — a single-transaction DROP-then-CREATE
-- errors with "being dropped, try again later" on CRDB.
--
-- Idempotent: IF NOT EXISTS / IF EXISTS guards keep the migration
-- safe whether or not a prior hand-applied partial equivalent (from
-- the deleted scripts/migrate_chat_reads_dm_index.go) is present.

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_reads_user_dm_v2
    ON chat_reads (user_id ASC, dm_room_id ASC);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS chat_reads@idx_chat_reads_user_dm CASCADE;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_reads_user_dm
    ON chat_reads (dm_room_id ASC);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS chat_reads@idx_chat_reads_user_dm_v2 CASCADE;
-- +goose StatementEnd
