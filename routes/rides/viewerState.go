package rides

import (
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/google/uuid"
)

// ViewerState is the single source of truth for "what UI should the
// client render for this (ride, viewer) pair?". Computing it on the
// server kills the conditional-rendering sprawl on the frontend (no
// more `if isHost && hasBooking && status === 'pending'` chains) and
// guarantees consistency across every screen that touches a ride.
//
// Values:
//   - host                 — viewer owns the ride
//   - confirmed_passenger  — viewer has an accepted booking
//   - pending_passenger    — viewer has a pending request
//   - rejected_passenger   — viewer's request was declined
//   - available            — viewer is neither host nor booked; seats free
//   - full                 — viewer hasn't booked and no seats free
//   - past                 — ride is more than 24h in the past
type ViewerState string

const (
	StateHost               ViewerState = "host"
	StateConfirmedPassenger ViewerState = "confirmed_passenger"
	StatePendingPassenger   ViewerState = "pending_passenger"
	StateRejectedPassenger  ViewerState = "rejected_passenger"
	StateAvailable          ViewerState = "available"
	StateFull               ViewerState = "full"
	StatePast               ViewerState = "past"
)

// ViewerActions is a flat capability map the frontend can read
// directly to drive button visibility. Keeping it as a struct instead
// of computing the booleans on the client means the source of truth
// for "can this user cancel?" lives in one place.
type ViewerActions struct {
	CanRequestSeat      bool `json:"can_request_seat"`
	CanCancelBooking    bool `json:"can_cancel_booking"`
	CanCancelRide       bool `json:"can_cancel_ride"`
	CanAcceptPassengers bool `json:"can_accept_passengers"`
	CanOpenChat         bool `json:"can_open_chat"`
	CanRate             bool `json:"can_rate"`
}

// ViewerContext bundles the precomputed state + actions so endpoints
// can attach it to a ride response as one block.
type ViewerContext struct {
	State   ViewerState   `json:"viewer_state"`
	Actions ViewerActions `json:"actions"`
	// BookingID is non-nil for any state where the viewer has a
	// booking (confirmed/pending/rejected) — the client uses it for
	// cancel calls without a separate round-trip.
	BookingID *string `json:"viewer_booking_id,omitempty"`
}

// ResolveViewerState computes ViewerContext for a single ride. The
// caller supplies the viewer's booking row (already-loaded) so this
// helper does zero DB work; pair it with `LoadViewerBookings` below
// when batching across many rides.
func ResolveViewerState(
	ride *models.Ride,
	viewerID uuid.UUID,
	viewerBooking *models.Booking,
) ViewerContext {
	now := time.Now()

	// Past: rides that started more than 24h ago are read-only.
	if ride.StartTime.Before(now.Add(-24 * time.Hour)) {
		return ViewerContext{State: StatePast, Actions: ViewerActions{CanOpenChat: true}}
	}

	// Host wins over any booking row (a host shouldn't be able to
	// "book" their own ride, but if a booking row exists we ignore
	// it for state-resolution purposes).
	if ride.HostUserID == viewerID {
		return ViewerContext{
			State: StateHost,
			Actions: ViewerActions{
				CanCancelRide:       ride.IsOngoing == 0,
				CanAcceptPassengers: true,
				CanOpenChat:         true,
			},
		}
	}

	if viewerBooking != nil {
		bookingID := viewerBooking.ID.String()
		switch viewerBooking.RequestStatus {
		case "accepted":
			return ViewerContext{
				State: StateConfirmedPassenger,
				Actions: ViewerActions{
					CanCancelBooking: ride.IsOngoing == 0,
					CanOpenChat:      true,
				},
				BookingID: &bookingID,
			}
		case "pending":
			return ViewerContext{
				State: StatePendingPassenger,
				Actions: ViewerActions{
					CanCancelBooking: true,
				},
				BookingID: &bookingID,
			}
		case "rejected":
			return ViewerContext{
				State:     StateRejectedPassenger,
				Actions:   ViewerActions{},
				BookingID: &bookingID,
			}
		}
	}

	// No booking, not host — check passenger-seat availability.
	// total_seats includes the host, while booked_seats counts
	// accepted passengers only; keep this aligned with helpers/seats.go.
	if !helpers.CanAcceptAnotherPassenger(ride.TotalSeats, ride.BookedSeats) {
		return ViewerContext{State: StateFull, Actions: ViewerActions{}}
	}

	return ViewerContext{State: StateAvailable, Actions: ViewerActions{CanRequestSeat: true}}
}

// LoadViewerBookings batch-fetches the viewer's booking rows for a
// list of ride IDs in a single query. Returns a map keyed by ride
// ID so callers can look up O(1) while iterating rides — eliminates
// the N+1 pattern that used to live in every endpoint that needed
// viewer state.
//
// Passing an empty rideIDs slice or a zero viewerID is safe; the
// returned map is just empty.
func LoadViewerBookings(viewerID uuid.UUID, rideIDs []uuid.UUID) map[uuid.UUID]*models.Booking {
	out := map[uuid.UUID]*models.Booking{}
	if viewerID == uuid.Nil || len(rideIDs) == 0 {
		return out
	}
	var bookings []models.Booking
	if err := database.Database.Db.
		Where("passenger_id = ? AND ride_id IN ?", viewerID, rideIDs).
		Find(&bookings).Error; err != nil {
		return out
	}
	for i := range bookings {
		b := bookings[i]
		out[b.RideID] = &b
	}
	return out
}
