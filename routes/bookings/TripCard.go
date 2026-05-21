package bookings

import (
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// TripCard represents an "active trip" that should surface on the
// passenger's home screen — either upcoming today, or recently
// happened and still un-dismissed (i.e. the user hasn't tapped Pay
// or No-show yet).
type TripCard struct {
	BookingID         string `json:"booking_id"`
	RideID            string `json:"ride_id"`
	HostUserID        string `json:"host_user_id"`
	HostName          string `json:"host_name"`
	HostProfilePicURL string `json:"host_profile_picture_url,omitempty"`
	HostUPIVPA        string `json:"host_upi_vpa,omitempty"` // optional; surfaced for deeplink
	StartLocation     string `json:"start_location"`
	EndLocation       string `json:"end_location"`
	StartTime         string `json:"start_time"`
	TotalPrice        int    `json:"total_price"`
	// State the client uses to pick which UI variant of the card:
	//   "upcoming"   — trip is in the future, info card only
	//   "in_window"  — between start_time and start_time+24h, show Pay button
	//   "stale"      — past 24h, collapsed compact reminder
	// After 7 days the row is filtered out server-side entirely.
	Stage string `json:"stage"`
}

// GetActiveTripCard returns the single most relevant TripCard for the
// current viewer — the trip they should be thinking about right now.
// Returns 204 if nothing fits the window (the client interprets that
// as "render nothing"; no UI noise).
//
// Selection (in priority order):
//  1. Next upcoming accepted booking starting in the next 12h.
//  2. Most recent accepted booking with start_time in the past 7d
//     that hasn't been dismissed (dismissed_at IS NULL).
//  3. None.
func GetActiveTripCard(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	card := BuildActiveTripCard(user.ID)
	if card == nil {
		return c.Status(fiber.StatusNoContent).Send(nil)
	}

	return c.JSON(fiber.Map{"trip_card": card})
}

func BuildActiveTripCard(userID uuid.UUID) *TripCard {
	now := time.Now()
	soon := now.Add(12 * time.Hour)
	weekAgo := now.Add(-7 * 24 * time.Hour)

	// Try the "upcoming within 12h" bucket first.
	upcoming, found := findCandidate(userID, "upcoming", now, soon, weekAgo)
	if found {
		return &upcoming
	}

	// Fall back to "recently happened, still un-dismissed" within 7d.
	recent, found := findCandidate(userID, "recent", now, soon, weekAgo)
	if found {
		return &recent
	}

	return nil
}

// findCandidate runs the booking lookup in the requested mode and
// transforms the matched booking + ride + host into a TripCard.
func findCandidate(viewerID uuid.UUID, mode string, now, soon, weekAgo time.Time) (TripCard, bool) {
	var booking models.Booking
	q := database.Database.Db.
		Where("passenger_id = ?", viewerID).
		Where("request_status = ?", "accepted")

	switch mode {
	case "upcoming":
		// Trips starting in the next 12 hours. Joining rides for the
		// time filter keeps it a single query.
		q = q.Joins("JOIN rides ON rides.id = bookings.ride_id").
			Where("rides.start_time BETWEEN ? AND ?", now, soon).
			Order("rides.start_time ASC")
	case "recent":
		// Trips that started in the last 7 days, haven't been
		// dismissed yet (passenger never tapped Pay / No-show).
		q = q.Joins("JOIN rides ON rides.id = bookings.ride_id").
			Where("rides.start_time BETWEEN ? AND ?", weekAgo, now).
			Where("bookings.dismissed_at IS NULL").
			Order("rides.start_time DESC")
	default:
		return TripCard{}, false
	}

	if err := q.First(&booking).Error; err != nil {
		return TripCard{}, false
	}

	var ride models.Ride
	if err := database.Database.Db.Where("id = ?", booking.RideID).First(&ride).Error; err != nil {
		return TripCard{}, false
	}

	var host models.User
	if err := database.Database.Db.
		Select("id, name, profile_picture_url, contact_number, upi_vpa").
		Where("id = ?", ride.HostUserID).
		First(&host).Error; err != nil {
		return TripCard{}, false
	}

	stage := "upcoming"
	switch {
	case ride.StartTime.After(now):
		stage = "upcoming"
	case ride.StartTime.After(now.Add(-24 * time.Hour)):
		stage = "in_window"
	default:
		stage = "stale"
	}

	return TripCard{
		BookingID:         booking.ID.String(),
		RideID:            ride.ID.String(),
		HostUserID:        host.ID.String(),
		HostName:          host.Name,
		HostProfilePicURL: host.ProfilePictureURL,
		HostUPIVPA:        host.UPIVPA,
		StartLocation:     ride.StartLocation,
		EndLocation:       ride.EndLocation,
		StartTime:         ride.StartTime.Format(time.RFC3339),
		TotalPrice:        int(ride.TotalPrice),
		Stage:             stage,
	}, true
}

// DismissTripCard records the passenger's post-trip action against a
// single booking. Idempotent — re-dismissing with the same signal
// is a no-op, but a different signal will overwrite (e.g. user marks
// paid then realises they want to flag no-show).
//
// Body:
//
//	{ "booking_id": "uuid", "signal": "paid"|"no_show"|"cancelled" }
//
// Signals feed the hidden reputation system off-band. The Pay
// deeplink itself is fired client-side; this endpoint just records
// the *intent*.
func DismissTripCard(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	type payload struct {
		BookingID string `json:"booking_id"`
		Signal    string `json:"signal"`
	}
	var body payload
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	signal := strings.ToLower(strings.TrimSpace(body.Signal))
	switch signal {
	case "paid", "no_show", "cancelled":
		// allowed
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "signal must be one of: paid, no_show, cancelled",
		})
	}

	bookingUUID, err := uuid.Parse(body.BookingID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid booking_id"})
	}

	// Confirm the booking belongs to the viewer — never let user A
	// dismiss user B's trip card.
	var booking models.Booking
	if err := database.Database.Db.
		Where("id = ? AND passenger_id = ?", bookingUUID, user.ID).
		First(&booking).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "booking not found"})
	}

	now := time.Now()
	booking.DismissedAt = &now
	booking.DismissalSignal = signal
	if err := database.Database.Db.Save(&booking).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to record dismissal",
		})
	}

	return c.JSON(fiber.Map{"status": "OK"})
}
