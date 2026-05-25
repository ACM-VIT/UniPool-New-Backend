-- +goose Up

-- Hot-path indexes for the performance pass.
--
-- 00001 contains a baseline schema snapshot, but existing production
-- databases are stamped at version 1 without replaying that DDL. This
-- migration explicitly creates the indexes the current query layer
-- relies on so older databases get the same planner support as fresh
-- installs.

-- /rides/nearby and /app/state nearby snapshot:
-- bounding-box predicates on start_latitude/start_longitude, future
-- start_time, open passenger capacity, and host exclusion.
CREATE INDEX IF NOT EXISTS idx_rides_open_nearby_lat_lng_v2
  ON rides (start_latitude ASC, start_longitude ASC, start_time ASC, host_user_id ASC)
  WHERE deleted_at IS NULL
    AND is_ongoing = 0
    AND start_latitude IS NOT NULL
    AND start_longitude IS NOT NULL
    AND booked_seats < (total_seats - 1);

-- /ride/matching-create: time-window scan with both pickup and drop
-- coordinates present. The live accepted-booking count still resolves
-- from bookings, but this keeps the candidate ride set tight first.
CREATE INDEX IF NOT EXISTS idx_rides_matching_create_window_v2
  ON rides (
    start_time ASC,
    start_latitude ASC,
    start_longitude ASC,
    end_latitude ASC,
    end_longitude ASC,
    host_user_id ASC
  )
  WHERE deleted_at IS NULL
    AND is_ongoing = 0
    AND start_latitude IS NOT NULL
    AND start_longitude IS NOT NULL
    AND end_latitude IS NOT NULL
    AND end_longitude IS NOT NULL;

-- Geography expression indexes for ST_DWithin/ST_Distance matching
-- paths. idx_rides_start_geog existed in the baseline snapshot; this
-- reasserts it for pre-goose databases and adds the symmetric dropoff
-- index that matching-create also needs.
CREATE INVERTED INDEX IF NOT EXISTS idx_rides_start_geog
  ON rides ((st_setsrid(st_makepoint(start_longitude::FLOAT8, start_latitude::FLOAT8), 4326)::GEOGRAPHY))
  WHERE (start_latitude IS NOT NULL) AND (start_longitude IS NOT NULL);

CREATE INVERTED INDEX IF NOT EXISTS idx_rides_end_geog_v2
  ON rides ((st_setsrid(st_makepoint(end_longitude::FLOAT8, end_latitude::FLOAT8), 4326)::GEOGRAPHY))
  WHERE (end_latitude IS NOT NULL) AND (end_longitude IS NOT NULL);

-- Profile/app-state stats: hosted completed rides and hosted ride
-- counts are keyed by host_user_id and bounded by start_time.
CREATE INDEX IF NOT EXISTS idx_rides_profile_host_completed_v2
  ON rides (host_user_id ASC, start_time DESC)
  WHERE deleted_at IS NULL
    AND is_ongoing <> 1;

-- /rides/involved and /user/rides: fast lookup of rides hosted by
-- the caller, ordered by recency/upcoming state.
CREATE INDEX IF NOT EXISTS idx_rides_host_time_active_v2
  ON rides (host_user_id ASC, start_time DESC)
  WHERE deleted_at IS NULL;

-- Booking summaries/profile stats frequently count or join active
-- bookings by passenger. Existing baseline indexes may not exist on
-- pre-goose databases, so create the narrow partial forms here.
CREATE INDEX IF NOT EXISTS idx_bookings_passenger_active_v2
  ON bookings (passenger_id ASC, ride_id ASC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bookings_passenger_status_active_v2
  ON bookings (passenger_id ASC, request_status ASC, ride_id ASC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bookings_ride_status_active_v2
  ON bookings (ride_id ASC, request_status ASC, passenger_id ASC)
  WHERE deleted_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_bookings_ride_status_active_v2;
DROP INDEX IF EXISTS idx_bookings_passenger_status_active_v2;
DROP INDEX IF EXISTS idx_bookings_passenger_active_v2;
DROP INDEX IF EXISTS idx_rides_host_time_active_v2;
DROP INDEX IF EXISTS idx_rides_profile_host_completed_v2;
DROP INDEX IF EXISTS idx_rides_end_geog_v2;
DROP INDEX IF EXISTS idx_rides_matching_create_window_v2;
DROP INDEX IF EXISTS idx_rides_open_nearby_lat_lng_v2;
