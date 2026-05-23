package helpers

// Single source of truth for seat semantics across the UniPool
// backend. Every route handler that gates bookings, computes "seats
// left", or projects capacity should go through this package
// instead of inlining `booked_seats < total_seats - 1` or similar.
// Keeping the predicate in one place stops the kind of slow drift
// where one handler enforces the new contract and another keeps
// the old one — which IS what happened pre-migration when
// total_seats meant "passenger seats" everywhere but the unequal
// split UI silently dropped the host's contribution.
//
// The contract (as of migration 00003_seats_include_host):
//
//   total_seats   — total people in the car, including the host
//                   driver. Migration 00003 bumped every existing
//                   row by +1 so the meaning is consistent across
//                   pre- and post-migration data.
//   booked_seats  — confirmed passenger bookings only. The host is
//                   NEVER counted here; their "seat" is implicit
//                   from being the host.
//   passenger capacity = total_seats - 1
//   seats left for booking = (total_seats - 1) - booked_seats
//   per-seat fare = total_fare / total_seats  (host pays a share
//   out of pocket; passengers reimburse the rest)
//
// SQL helpers live alongside the Go helpers so query-builders can
// drop the same predicate string into a Where() without rebuilding
// it from raw column references. If you change the meaning of
// either column in the future, change it here and grep for
// references — every handler should be touching this package.

// PassengerCapacity returns how many passenger seats a ride has,
// given the total_seats value from the DB. Returns 0 for an
// invalid (zero) total — never negative, since callers compare
// this against uint counts.
func PassengerCapacity(totalSeats uint) uint {
	if totalSeats == 0 {
		return 0
	}
	return totalSeats - 1
}

// PassengerSeatsLeft is the count of bookable passenger slots
// remaining: passenger capacity minus already-confirmed bookings.
// Clamped at zero so a row with a corrupt booked_seats > capacity
// reads as "full" rather than negative.
func PassengerSeatsLeft(totalSeats, bookedSeats uint) uint {
	cap := PassengerCapacity(totalSeats)
	if bookedSeats >= cap {
		return 0
	}
	return cap - bookedSeats
}

// CanAcceptAnotherPassenger gates booking acceptance. True iff
// there's at least one passenger slot left. Use this BEFORE any
// transaction that increments booked_seats.
func CanAcceptAnotherPassenger(totalSeats, bookedSeats uint) bool {
	return PassengerSeatsLeft(totalSeats, bookedSeats) > 0
}

// PassengerSeatsLeftPredicate is the SQL fragment to gate a query
// to rides that still have a passenger slot open. Includes the
// `- 1` adjustment to discount the host's seat from total_seats.
//
// Use as a Where() arg:
//
//	tx.Where(helpers.PassengerSeatsLeftPredicate)
//
// (without binding any args; the predicate is pure column arithmetic.)
const PassengerSeatsLeftPredicate = "booked_seats < total_seats - 1"

// MinSeatsLeftPredicate is the SQL fragment used by /ride/search's
// `min_seats` filter. Mirrors PassengerSeatsLeftPredicate but
// parameterised on the requested seat count. The single `?`
// placeholder binds to the minimum number of seats the caller
// needs (e.g. a group of 3 looking for a ride together).
//
//	tx.Where(helpers.MinSeatsLeftPredicate, requestedSeats)
const MinSeatsLeftPredicate = "(total_seats - 1 - booked_seats) >= ?"

// MinTotalSeats is the smallest legal value for a new ride's
// total_seats — host + one passenger. Anything less means there's
// nobody for the host to share the ride with, which would make
// the ride pointless. Enforced at /ride/create.
const MinTotalSeats uint = 2

// MaxTotalSeats is a sanity cap — even a Tata Winger / 7-seat SUV
// driving + 7 passengers is 8. The number isn't strictly enforced
// today (the create form's stepper handles UX bounds) but the
// constant lives here so a future server-side validator can
// reference it without picking a different number.
const MaxTotalSeats uint = 9
