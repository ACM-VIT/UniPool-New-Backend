package bookings

import (
	"fmt"
	"log"
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/routes/chat"

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

	// Pull at most one row per candidate bucket before joining the
	// host. That keeps the hot home snapshot bounded even for users
	// with a long accepted-booking history: one upcoming candidate
	// and one recent-undismissed candidate, then a two-row final sort.
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
	err := database.Database.Db.Raw(`
		WITH upcoming AS (
			SELECT
				b.id AS booking_id,
				r.id AS ride_id,
				r.host_user_id,
				r.start_location,
				r.end_location,
				r.start_time,
				r.total_price,
				0 AS bucket
			  FROM rides r
			  JOIN bookings b
			    ON b.ride_id = r.id
			   AND b.passenger_id = ?
			   AND b.request_status = 'accepted'
			   AND b.deleted_at IS NULL
			 WHERE r.deleted_at IS NULL
			   AND r.host_user_id <> b.passenger_id
			   AND r.start_time BETWEEN ? AND ?
			 ORDER BY r.start_time ASC
			 LIMIT 1
		),
		recent AS (
			SELECT
				b.id AS booking_id,
				r.id AS ride_id,
				r.host_user_id,
				r.start_location,
				r.end_location,
				r.start_time,
				r.total_price,
				1 AS bucket
			  FROM rides r
			  JOIN bookings b
			    ON b.ride_id = r.id
			   AND b.passenger_id = ?
			   AND b.request_status = 'accepted'
			   AND b.deleted_at IS NULL
			 WHERE r.deleted_at IS NULL
			   AND r.host_user_id <> b.passenger_id
			   AND b.dismissed_at IS NULL
			   AND r.start_time >= ?
			   AND r.start_time < ?
			 ORDER BY r.start_time DESC
			 LIMIT 1
		),
		candidate AS (
			SELECT * FROM upcoming
			UNION ALL
			SELECT * FROM recent
		)
		SELECT
			c.booking_id,
			c.ride_id,
			u.id AS host_user_id,
			u.name AS host_name,
			u.profile_picture_url AS host_profile_picture_url,
			u.upi_vpa AS host_upi_vpa,
			c.start_location,
			c.end_location,
			c.start_time,
			c.total_price
		  FROM candidate c
		  JOIN users u ON u.id = c.host_user_id
		 ORDER BY c.bucket ASC
		 LIMIT 1
	`, userID, now, soon, userID, weekAgo, now).Scan(&hit).Error
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

	// Confirm the booking belongs to the viewer and pull the amount
	// needed by the optional chat marker in the same projected read.
	type dismissalRow struct {
		BookingID  uuid.UUID `gorm:"column:booking_id"`
		RideID     uuid.UUID `gorm:"column:ride_id"`
		TotalPrice uint      `gorm:"column:total_price"`
	}
	var dismissal dismissalRow
	if err := database.Database.Db.
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			b.ride_id,
			COALESCE(r.total_price, 0) AS total_price
		`).
		Joins("LEFT JOIN rides r ON r.id = b.ride_id AND r.deleted_at IS NULL").
		Where("b.id = ? AND b.passenger_id = ? AND b.deleted_at IS NULL", bookingUUID, user.ID).
		Scan(&dismissal).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to load booking"})
	}
	if dismissal.BookingID == (uuid.UUID{}) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "booking not found"})
	}

	now := time.Now()
	updates := map[string]any{
		"dismissed_at":     &now,
		"dismissal_signal": signal,
	}
	// When the passenger marks paid, also flip the payment state to
	// 'pending' so the host has something to confirm against. The
	// other signals leave payment_status alone — "no_show" and
	// "cancelled" don't imply a payment ever happened.
	if signal == "paid" {
		updates["payment_status"] = "pending"
	}

	firstDismiss := false
	claimFirstDismiss := database.Database.Db.
		Model(&models.Booking{}).
		Where("id = ? AND passenger_id = ? AND dismissed_at IS NULL", dismissal.BookingID, user.ID).
		Updates(updates)
	if claimFirstDismiss.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to record dismissal",
		})
	}
	firstDismiss = claimFirstDismiss.RowsAffected > 0

	if !firstDismiss {
		rewrite := database.Database.Db.
			Model(&models.Booking{}).
			Where("id = ? AND passenger_id = ?", dismissal.BookingID, user.ID).
			Updates(updates)
		if rewrite.Error != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to record dismissal",
			})
		}
		if rewrite.RowsAffected == 0 {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "booking not found"})
		}
	}

	// Post a payment_marker into the ride group chat so the host
	// sees the declaration and can tap Confirm received / Didn't
	// receive inline. Best-effort: a chat insert failure shouldn't
	// fail the whole dismiss flow — the dismissal_signal is already
	// recorded and we'll just be missing the chat surface. Fire
	// only on the first "paid" dismissal (no_show and cancelled
	// don't belong in the host's chat; re-dismisses are idempotent).
	if signal == "paid" && firstDismiss {
		amount := dismissal.TotalPrice
		passengerName := user.Name
		content := fmt.Sprintf("%s marked their seat as paid (₹%d).", passengerName, amount)
		meta := models.MessageMetadata{
			"booking_id":     dismissal.BookingID.String(),
			"passenger_id":   user.ID.String(),
			"passenger_name": passengerName,
			"amount":         amount,
		}
		if _, err := chat.PostSystemMessage(
			dismissal.RideID,
			user.ID,
			models.MessageKindPaymentMarker,
			content,
			meta,
			"Payment marked",
			fmt.Sprintf("%s says they paid ₹%d. Confirm in the trip chat.", passengerName, amount),
		); err != nil {
			log.Printf("DismissTripCard: payment_marker post failed for booking %s: %v", dismissal.BookingID, err)
		}
	}

	return c.JSON(fiber.Map{"status": "OK"})
}

// PaymentAck is the host's response to a payment_marker. Flips the
// booking's payment_status to confirmed or disputed and posts a
// payment_ack system message into the ride chat so the passenger and
// every other accepted rider see the outcome.
//
// Route: POST /booking/:id/payment-ack
// Body:  { "ack": "received" | "missing" }
// Auth:  must be the host of the booking's ride.
func PaymentAck(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	bookingUUID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid booking id"})
	}

	var body struct {
		Ack string `json:"ack"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	ack := strings.ToLower(strings.TrimSpace(body.Ack))
	if ack != "received" && ack != "missing" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "ack must be 'received' or 'missing'",
		})
	}

	// Load booking, ride authorization fields, and passenger copy in
	// one projected read instead of hydrating three models.
	type paymentAckRow struct {
		BookingID     uuid.UUID `gorm:"column:booking_id"`
		RideID        uuid.UUID `gorm:"column:ride_id"`
		PassengerID   uuid.UUID `gorm:"column:passenger_id"`
		HostUserID    uuid.UUID `gorm:"column:host_user_id"`
		TotalPrice    uint      `gorm:"column:total_price"`
		PassengerName string    `gorm:"column:passenger_name"`
	}
	var payment paymentAckRow
	if err := database.Database.Db.
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			r.host_user_id,
			r.total_price,
			COALESCE(p.name, '') AS passenger_name
		`).
		Joins("JOIN rides r ON r.id = b.ride_id AND r.deleted_at IS NULL").
		Joins("LEFT JOIN users p ON p.id = b.passenger_id AND p.deleted_at IS NULL").
		Where("b.id = ? AND b.deleted_at IS NULL", bookingUUID).
		Scan(&payment).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to load booking"})
	}
	if payment.BookingID == (uuid.UUID{}) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "booking not found"})
	}
	if payment.HostUserID != user.ID {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "only the host can confirm payments for this ride",
		})
	}

	passengerName := payment.PassengerName
	if passengerName == "" {
		passengerName = "Passenger"
	}

	now := time.Now()
	paymentStatus := map[string]string{
		"received": "confirmed",
		"missing":  "disputed",
	}[ack]
	ackUpdate := database.Database.Db.
		Model(&models.Booking{}).
		Where("id = ?", payment.BookingID).
		Updates(map[string]any{
			"payment_status":       paymentStatus,
			"payment_confirmed_at": &now,
		})
	if ackUpdate.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to record acknowledgement",
		})
	}
	if ackUpdate.RowsAffected == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "booking not found"})
	}

	// System message stating the outcome. Best-effort like the
	// payment_marker insert above.
	var content, fcmTitle, fcmBody string
	if ack == "received" {
		content = fmt.Sprintf("%s confirmed receiving ₹%d from %s.", user.Name, payment.TotalPrice, passengerName)
		fcmTitle = "Payment confirmed"
		fcmBody = fmt.Sprintf("%s confirmed your payment of ₹%d.", user.Name, payment.TotalPrice)
	} else {
		content = fmt.Sprintf("%s hasn't received ₹%d from %s yet.", user.Name, payment.TotalPrice, passengerName)
		fcmTitle = "Payment not received"
		fcmBody = fmt.Sprintf("%s couldn't find ₹%d from you. Check with them in the trip chat.", user.Name, payment.TotalPrice)
	}
	meta := models.MessageMetadata{
		"booking_id": payment.BookingID.String(),
		"ack":        ack,
		"amount":     payment.TotalPrice,
	}
	if _, err := chat.PostSystemMessage(
		payment.RideID,
		user.ID,
		models.MessageKindPaymentAck,
		content,
		meta,
		fcmTitle,
		fcmBody,
	); err != nil {
		log.Printf("PaymentAck: payment_ack post failed for booking %s: %v", payment.BookingID, err)
	}

	return c.JSON(fiber.Map{
		"status":               "OK",
		"payment_status":       paymentStatus,
		"payment_confirmed_at": &now,
	})
}
