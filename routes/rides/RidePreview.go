package rides

import (
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// PreviewResponse is the sanitised view of a ride that powers the
// public share landing page at unipool.acmvit.in/ride/:id.
//
// Compared with GetRideDetailsComplete this intentionally omits:
//   - everything passenger-related (bookings, contact numbers, UPI VPAs)
//   - exact GPS coordinates (start/end strings are enough for a poster)
//   - the host's email, phone, profile picture URL, full last name
//   - is_ongoing / booking states
//
// The goal is: a recipient who hasn't installed the app can scan the
// link, see "Vellore → Chennai, Fri 23 May at 1pm, ₹220 a seat, hosted
// by Anika", and decide whether to install. That's the entire surface
// — anything richer requires sign-in.
type PreviewResponse struct {
	ID                string `json:"id"`
	StartLocation     string `json:"start_location"`
	EndLocation       string `json:"end_location"`
	StartTime         string `json:"start_time"`
	TotalSeats        uint   `json:"total_seats"`
	BookedSeats       uint   `json:"booked_seats"`
	SeatsAvailable    uint   `json:"seats_available"`
	TotalPrice        uint   `json:"total_price"`
	PricePerSeat      uint   `json:"price_per_seat"`
	IsSameGender      bool   `json:"is_same_gender"`
	HostFirstName     string `json:"host_first_name"`
	HostInstituteName string `json:"host_institute_name,omitempty"`
}

// GetRidePreview returns the sanitised PreviewResponse for the given
// ride UUID. Public — no auth header required. Used exclusively by the
// share landing page; the in-app ride details screen still uses the
// gated /ride/details/:id endpoint which returns the full surface.
//
// Status codes:
//
//	400 — :id is not a UUID
//	404 — no ride with that ID exists, or the host has been deleted
//	500 — anything else
//
// Performance: one projected SELECT against rides + host + institute.
func GetRidePreview(c *fiber.Ctx) error {
	idParam := c.Params("id")
	rideID, err := uuid.Parse(idParam)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid ride ID",
		})
	}

	db := database.Database.Db

	type previewRow struct {
		ID                uuid.UUID `gorm:"column:id"`
		StartLocation     string    `gorm:"column:start_location"`
		EndLocation       string    `gorm:"column:end_location"`
		StartTime         time.Time `gorm:"column:start_time"`
		TotalSeats        uint      `gorm:"column:total_seats"`
		BookedSeats       uint      `gorm:"column:booked_seats"`
		TotalPrice        uint      `gorm:"column:total_price"`
		IsSameGender      uint      `gorm:"column:is_same_gender"`
		HostName          string    `gorm:"column:host_name"`
		HostInstituteName *string   `gorm:"column:host_institute_name"`
	}
	var row previewRow
	result := db.Table("rides AS r").
		Select(`
			r.id,
			r.start_location,
			r.end_location,
			r.start_time,
			r.total_seats,
			r.booked_seats,
			r.total_price,
			r.is_same_gender,
			u.name AS host_name,
			i.name AS host_institute_name
		`).
		Joins("JOIN users AS u ON u.id = r.host_user_id").
		Joins("LEFT JOIN institutes AS i ON i.id = u.institute_id").
		Where("r.id = ?", rideID).
		Scan(&row)
	if result.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch ride",
		})
	}
	if result.RowsAffected == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	firstName := firstNameOf(row.HostName)

	// Ride.TotalPrice is the passenger-facing per-seat amount in the
	// mobile app. Keep the preview explicit so the launch site doesn't
	// divide it again and advertise a bogus underpriced ride.
	pricePerSeat := row.TotalPrice
	seatsAvailable := helpers.PassengerSeatsLeft(row.TotalSeats, row.BookedSeats)

	resp := PreviewResponse{
		ID:             row.ID.String(),
		StartLocation:  row.StartLocation,
		EndLocation:    row.EndLocation,
		StartTime:      row.StartTime.UTC().Format("2006-01-02T15:04:05Z"),
		TotalSeats:     row.TotalSeats,
		BookedSeats:    row.BookedSeats,
		SeatsAvailable: seatsAvailable,
		TotalPrice:     row.TotalPrice,
		PricePerSeat:   pricePerSeat,
		IsSameGender:   row.IsSameGender == 1,
		HostFirstName:  firstName,
	}
	if row.HostInstituteName != nil {
		resp.HostInstituteName = *row.HostInstituteName
	}

	// Strip cache-buster surfaces. The share poster is allowed to be
	// stale by up to 60s — that's the difference between "5 seats left"
	// and "4 seats left", which the recipient is going to verify inside
	// the app anyway.
	c.Set("Cache-Control", "public, max-age=60")

	return c.JSON(resp)
}

// firstNameOf returns the part of the user's name before the first
// space. Lets the share poster read "hosted by Anika" instead of
// "hosted by Anika Sharma Pillai" — the full name is private surface
// reserved for in-app interactions where the host has chosen to share
// it.
func firstNameOf(full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return ""
	}
	if i := strings.IndexAny(full, " \t"); i > 0 {
		return full[:i]
	}
	return full
}
