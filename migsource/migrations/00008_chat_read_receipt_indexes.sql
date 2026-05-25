-- +goose Up

-- Message history now emits read_by from chat_reads so reopened chats
-- keep persisted seen receipts. These indexes keep that per-page
-- lookup bounded to the current room instead of scanning read cursors.
CREATE INDEX IF NOT EXISTS idx_chat_reads_ride_seen_v2
  ON chat_reads (ride_id ASC, last_read_at ASC, user_id ASC)
  WHERE deleted_at IS NULL
    AND ride_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_chat_reads_dm_seen_v2
  ON chat_reads (dm_room_id ASC, last_read_at ASC, user_id ASC)
  WHERE deleted_at IS NULL
    AND dm_room_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_chat_reads_dm_seen_v2;
DROP INDEX IF EXISTS idx_chat_reads_ride_seen_v2;
