-- +goose Up

-- Migration: trip-today email dedup at the (ride, user) granularity,
-- plus a cooldown marker for ride-update push notifications.
--
-- WHY THE TRIP-TODAY DEDUP IS BEING REWORKED
--
-- 00005 added rides.trip_today_email_sent_at as a single boolean-ish
-- timestamp on the ride row. That worked for the common case but
-- broke a real flow:
--   1. Host posts ride for 9am tomorrow
--   2. At 8pm tonight the scheduler tick fires → host + currently
--      accepted passengers all get the email; ride row marked sent
--   3. At 10pm the host accepts a late requester
--   4. The late passenger NEVER gets the email — the ride row's
--      "sent" marker shortcuts the whole loop next tick
--
-- New table trip_today_emails_sent tracks (ride_id, user_id) pairs
-- with sent_at. The scheduler now iterates recipients and INSERTs
-- with ON CONFLICT DO NOTHING — the row insert IS the lease. Late-
-- accepted passengers get a row when their first tick after accept
-- catches the ride, no special path needed.
--
-- Drop the per-ride column entirely. It was only ever a stop-gap
-- and keeping it would just be two sources of truth for the same
-- "have we emailed yet" question.

CREATE TABLE IF NOT EXISTS trip_today_emails_sent (
  ride_id UUID NOT NULL,
  user_id UUID NOT NULL,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (ride_id, user_id),
  CONSTRAINT fk_trip_today_emails_ride
    FOREIGN KEY (ride_id) REFERENCES rides(id) ON DELETE CASCADE,
  CONSTRAINT fk_trip_today_emails_user
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Index for the "have I already emailed this user about this ride"
-- lookup path. The PK already covers (ride_id, user_id) so a
-- separate index on user_id alone lets us answer "all rides we've
-- emailed this user about" without a full PK scan if we ever need
-- to surface that on a settings screen.
CREATE INDEX IF NOT EXISTS idx_trip_today_emails_user
  ON trip_today_emails_sent (user_id);

-- Retire the per-ride sent marker + its partial index.
DROP INDEX IF EXISTS idx_rides_trip_today_email_pending;
ALTER TABLE rides DROP COLUMN IF EXISTS trip_today_email_sent_at;


-- COOLDOWN COLUMN FOR RIDE-UPDATE PUSH NOTIFICATIONS
--
-- A host can edit a ride's start_time / locations / fare repeatedly
-- to correct typos or finalize a plan. Without a cooldown each
-- edit pushes a "ride was updated" notification to every accepted
-- passenger — 3 edits in 5 minutes = 3 pings.
--
-- update_notif_last_sent_at gates the fan-out: the routes/CRUD
-- update handler skips the push if this timestamp is within the
-- last 5 minutes, and stamps it on every successful send. The
-- next edit's window opens 5 min after the previous push.

ALTER TABLE rides
  ADD COLUMN IF NOT EXISTS update_notif_last_sent_at TIMESTAMPTZ;


-- +goose Down

ALTER TABLE rides DROP COLUMN IF EXISTS update_notif_last_sent_at;

ALTER TABLE rides ADD COLUMN IF NOT EXISTS trip_today_email_sent_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_rides_trip_today_email_pending
  ON rides (start_time)
  WHERE trip_today_email_sent_at IS NULL;

DROP INDEX IF EXISTS idx_trip_today_emails_user;
DROP TABLE IF EXISTS trip_today_emails_sent;
