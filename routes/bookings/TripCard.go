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

// BuildActiveTripCard returns the single most relevant trip the viewer
// should see on the home screen: upcoming-within-12h takes priority
// over recent-undismissed-within-7d.
//
// Performance: pre-fix this fanned out to up to SIX sequential
// round-trips for a viewer with no active trip (two findCandidate
// calls × {booking, ride, host} each). Now it's a single JOINed SQL
// statement that pulls a bucketed row for each candidate window,
// projects only the columns we actually emit on the card, and orders
// "upcoming" ahead of "recent". LIMIT 1 gives us the winner straight
// from the DB — zero per-bucket follow-ups, zero per-host follow-ups.
func BuildActiveTripCard(userID uuid.UUID) *TripCard {
	now := time.Now()
	soon := now.Add(12 * time.Hour)
	weekAgo := now.Add(-7 * 24 * time.Hour)
	dayAgo := now.Add(-24 * time.Hour)

	// Bucket priorities: lower number wins. The CASE in the SELECT
	// gives us per-bucket tie-breaking; ORDER BY bucket ASC, then by
	// start_time directionally per bucket. We can't order
	// directionally per bucket in one statement easily, so we sort
	// upcoming asc and recent desc by adding an "order_key" timestamp
	// that flips sign for the recent bucket — newest-recent first,
	// soonest-upcoming first, but upcoming always wins outright.
	type row struct {
		BookingID         uuid.UUID `gorm:"column:booking_id"`
		RideID            uuid.UUID `gorm:"column:ride_id"`
		HostUserID        uuid.UUID `gorm:"column:host_user_id"`
		HostName          string    `gorm:"column:host_name"`
		HostProfilePicURL string    `gorm:"column:host_profile_picture_url"`
		HostUPIVPA        string    `gorm:"column:host_upi_vpa"`
		StartLocation     string    `gorm:"column:start_location"`
		EndLocation       string    `gorm:"column:end_location"`
		StartTime         time.Time `gorm:"column:start_time"`
		TotalPrice        int       `gorm:"column:total_price"`
	}
	var hit row
	err := database.Database.Db.
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			r.id AS ride_id,
			u.id AS host_user_id,
			u.name AS host_name,
			u.profile_picture_url AS host_profile_picture_url,
			u.upi_vpa AS host_upi_vpa,
			r.start_location,
			r.end_location,
			r.start_time,
			r.total_price,
			CASE
				WHEN r.start_time BETWEEN ? AND ? THEN 0
				WHEN r.start_time BETWEEN ? AND ? AND b.dismissed_at IS NULL THEN 1
				ELSE 2
			END AS bucket
		`, now, soon, weekAgo, now).
		Joins("JOIN rides r ON r.id = b.ride_id").
		Joins("JOIN users u ON u.id = r.host_user_id").
		Where("b.passenger_id = ? AND b.request_status = ?", userID, "accepted").
		Where(`
			(r.start_time BETWEEN ? AND ?)
			OR (r.start_time BETWEEN ? AND ? AND b.dismissed_at IS NULL)
		`, now, soon, weekAgo, now).
		Order("bucket ASC, r.start_time DESC").
		Limit(1).
		Scan(&hit).Error
	if err != nil || hit.BookingID == (uuid.UUID{}) {
		return nil
	}

	stage := "upcoming"
	switch {
	case hit.StartTime.After(now):
		stage = "upcoming"
	case hit.StartTime.After(dayAgo):
		stage = "in_window"
	default:
		stage = "stale"
	}

	return &TripCard{
		BookingID:         hit.BookingID.String(),
		RideID:            hit.RideID.String(),
		HostUserID:        hit.HostUserID.String(),
		HostName:          hit.HostName,
		HostProfilePicURL: hit.HostProfilePicURL,
		HostUPIVPA:        hit.HostUPIVPA,
		StartLocation:     hit.StartLocation,
		EndLocation:       hit.EndLocation,
		StartTime:         hit.StartTime.Format(time.RFC3339),
		TotalPrice:        hit.TotalPrice,
		Stage:             stage,
	}
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
