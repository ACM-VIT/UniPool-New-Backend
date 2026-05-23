-- +goose NO TRANSACTION
-- +goose Up

-- Migration: seat semantics now include the host driver.
--
-- Prior to this migration, total_seats meant "number of passenger
-- seats the host is offering" and the host was never counted.
-- That made the fare-splitting math wrong: totalFare / passengerCount
-- left the host paying nothing for their own ride (passengers
-- effectively subsidising the host's fuel + tolls). It also made
-- the unequal-split UI dishonest because there was no row for the
-- host's own contribution.
--
-- New contract (matches helpers/seats.go):
--   total_seats  = total people in the car, host + passengers
--   booked_seats = confirmed passenger bookings only (host never
--                  counted)
--   passenger capacity = total_seats - 1
--   seats left for booking = (total_seats - 1) - booked_seats
--
-- The booking-acceptance gate moves from `booked_seats < total_seats`
-- to `booked_seats < total_seats - 1` in code; this migration brings
-- existing data into the new world so that gate produces the same
-- "has seats?" answer before and after the deploy.
--
-- Concretely: a ride that pre-migration was total_seats=3 (3
-- passenger seats), booked_seats=1 had 2 passenger seats left. Post-
-- migration that row reads total_seats=4 (host + 3 passengers),
-- booked_seats=1, with `total_seats - 1 - booked_seats` = 2. Same
-- answer, consistent semantics.
--
-- Edge cases:
--   * Fully-booked rides (booked_seats == total_seats pre-mig) stay
--     fully booked post-mig: total_seats=N+1, booked_seats=N,
--     `N+1 - 1 - N` = 0.
--   * Rides with the legacy total_seats=0 (never legal, but defensive)
--     would jump to 1 — still "no passenger seats", consistent.
--   * The migration is idempotent only against unmigrated rows: a
--     second run would over-bump. Goose's version table guards
--     against that on every well-behaved deploy path; if someone
--     runs the migration manually outside `unipool-backend migrate`
--     they need to know what they're doing.

-- +goose StatementBegin
UPDATE rides
   SET total_seats = total_seats + 1;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
UPDATE rides
   SET total_seats = GREATEST(0, total_seats - 1);
-- +goose StatementEnd
