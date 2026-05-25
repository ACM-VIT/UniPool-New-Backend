-- +goose Up

-- /app/state active trip card now scans rides by two tight start_time
-- windows before joining the viewer's accepted booking. Keep that
-- ride-window scan index-backed on databases that rely on goose
-- migrations instead of the startup index bootstrap.
CREATE INDEX IF NOT EXISTS idx_rides_start_time_active_v2
  ON rides (start_time ASC, id ASC, host_user_id ASC)
  WHERE deleted_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_rides_start_time_active_v2;
