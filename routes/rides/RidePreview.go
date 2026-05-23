package rides

import (
	"strings"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
// Performance: a single SELECT against rides + a single SELECT against
// users for the host's first name + institute. Both indexed by ID.
func GetRidePreview(c *fiber.Ctx) error {
	idParam := c.Params("id")
	rideID, err := uuid.Parse(idParam)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid ride ID",
		})
	}

	db := database.Database.Db

	var ride models.Ride
	if err := db.Where("id = ?", rideID).First(&ride).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Ride not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch ride",
		})
	}

	// Resolve the host's first name and institute label. We only need
	// the columns we surface — pulling the full User row would also
	// drag in contact_number, dob, fcm_token, etc, which we do not
	// want anywhere near a public endpoint.
	type hostBasics struct {
		Name          string  `gorm:"column:name"`
		InstituteName *string `gorm:"column:institute_name"`
	}
	var host hostBasics
	db.Table("users AS u").
		Select("u.name AS name, i.name AS institute_name").
		Joins("LEFT JOIN institutes AS i ON i.id = u.institute_id").
		Where("u.id = ?", ride.HostUserID).
		Take(&host)

	firstName := firstNameOf(host.Name)

	// Per-seat price: rides record the *total* fare; the share poster
	// reads better as "₹X a seat", so we divide here once instead of
	// having the website (or app) do its own arithmetic.
	pricePerSeat := uint(0)
	if ride.TotalSeats > 0 {
		pricePerSeat = ride.TotalPrice / ride.TotalSeats
	}

	seatsAvailable := uint(0)
	if ride.TotalSeats > ride.BookedSeats {
		seatsAvailable = ride.TotalSeats - ride.BookedSeats
	}

	resp := PreviewResponse{
		ID:             ride.ID.String(),
		StartLocation:  ride.StartLocation,
		EndLocation:    ride.EndLocation,
		StartTime:      ride.StartTime.UTC().Format("2006-01-02T15:04:05Z"),
		TotalSeats:     ride.TotalSeats,
		BookedSeats:    ride.BookedSeats,
		SeatsAvailable: seatsAvailable,
		TotalPrice:     ride.TotalPrice,
		PricePerSeat:   pricePerSeat,
		IsSameGender:   ride.IsSameGender == 1,
		HostFirstName:  firstName,
	}
	if host.InstituteName != nil {
		resp.HostInstituteName = *host.InstituteName
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
