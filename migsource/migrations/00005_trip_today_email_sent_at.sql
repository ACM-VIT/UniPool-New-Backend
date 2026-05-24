-- +goose Up

-- Migration: dedup column for the day-before "your trip is tomorrow"
-- email.
--
-- The notification scheduler runs every 10 minutes and walks rides
-- in a small window of upcoming start_times. Without a sent-marker
-- column, a scheduler restart (or any tick that re-catches the same
-- ride in the window) would re-send the email — same problem the
-- old ride-reminder push had at 23/20 minutes before takeoff.
--
-- Using a timestamp instead of a boolean so a future "resend"
-- scenario (e.g. ride time edited after the email already fired)
-- can compare it against `updated_at` to decide whether to fire
-- again. Indexed on (start_time, trip_today_email_sent_at) because
-- the scheduler's predicate is `start_time BETWEEN ? AND ? AND
-- trip_today_email_sent_at IS NULL` — without the partial index
-- the row scan would re-read every upcoming ride per tick.

ALTER TABLE rides
  ADD COLUMN IF NOT EXISTS trip_today_email_sent_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_rides_trip_today_email_pending
  ON rides (start_time)
  WHERE trip_today_email_sent_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_rides_trip_today_email_pending;
ALTER TABLE rides DROP COLUMN IF EXISTS trip_today_email_sent_at;
