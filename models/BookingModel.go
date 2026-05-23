package models

import (
	"time"

	"github.com/google/uuid"
)

// Booking struct
type Booking struct {
	BaseModel
	RideID        uuid.UUID `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"ride_id" valid:"required~Ride ID is required"`
	Ride          Ride      `gorm:"foreignKey:RideID;references:ID;constraint:OnDelete:CASCADE" json:"ride" valid:"-"`
	PassengerID   uuid.UUID `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"passenger_id" valid:"required~Passenger ID is required"`
	Passenger     User      `gorm:"foreignKey:PassengerID;references:ID;constraint:OnDelete:CASCADE" json:"passenger" valid:"-"`
	RequestStatus string    `gorm:"type:varchar(10);not null" json:"request_status" valid:"required~Status is required,in(pending|accepted|rejected)~Status must be accepted or pending or rejected"`

	// Post-trip dismissal — set when the passenger closes the
	// "Trip with X · Pay ₹Y" home card. The signal is the only piece
	// of completion data we actually have (we never ask hosts to
	// mark trips done — they won't).
	//
	// Signal values:
	//   ""           — card still active (or auto-expired silently)
	//   "paid"       — passenger tapped Pay → UPI deeplink fired
	//   "no_show"    — passenger reported "didn't happen"
	//   "cancelled"  — passenger cancelled before the trip
	//
	// These feed the hidden reputation system. The "paid" signal is
	// optimistic (we can't verify UPI completed) but the tap itself
	// is strong intent — gamers would just not tap and lose rep.
	DismissedAt     *time.Time `gorm:"index" json:"dismissed_at,omitempty"`
	DismissalSignal string     `gorm:"type:varchar(20);default:''" json:"dismissal_signal,omitempty"`

	// Payment confirmation lifecycle, separate from DismissalSignal
	// because that's a passenger-side off-band reputation signal
	// while these track the host-confirmed state of an actual
	// transaction. State machine:
	//
	//   "none"      — default. No payment expected/pending.
	//   "pending"   — passenger declared paid (tapped Pay on the
	//                 trip card OR sent a payment_marker into chat).
	//                 Host hasn't acknowledged yet.
	//   "confirmed" — host tapped "Confirm received" on the chat
	//                 card. payment_confirmed_at stamps the moment.
	//   "disputed"  — host tapped "Didn't receive". Stamped too.
	//
	// Used by the chat card renderer and the host's per-trip
	// "who's paid" surface.
	PaymentStatus      string     `gorm:"type:varchar(16);not null;default:'none'" json:"payment_status"`
	PaymentConfirmedAt *time.Time `json:"payment_confirmed_at,omitempty"`
}
