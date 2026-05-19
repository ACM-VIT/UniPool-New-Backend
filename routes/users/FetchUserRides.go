package users

import (
	"context"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/routes/rides"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// UserRidesResponse is one row in the /user/rides payload. Carries
// enough to render a trip card AND the server-computed viewer state
// so the client doesn't have to derive "am I host? booked?" again.
type UserRidesResponse struct {
	RideID         uuid.UUID  `json:"ride_id"`
	HostUserID     uuid.UUID  `json:"host_user_id"`
	StartLocation  string     `json:"start_location"`
	EndLocation    string     `json:"end_location"`
	StartTime      time.Time  `json:"start_time"`
	TotalSeats     uint       `json:"total_seats"`
	BookedSeats    uint       `json:"booked_seats"`
	TotalPrice     uint       `json:"total_price"`
	IsOngoing      uint       `json:"is_ongoing"`
	IsSameGender   uint       `json:"is_same_gender"`
	PassengerID    *uuid.UUID `json:"passenger_id,omitempty"`
	RequestStatus  string     `json:"request_status,omitempty"`
	IsUserHost     bool       `json:"is_user_host"`

	// Server-computed UI state — same shape as on /ride/details/:id.
	ViewerState     rides.ViewerState   `json:"viewer_state"`
	Actions         rides.ViewerActions `json:"actions"`
	ViewerBookingID *string             `json:"viewer_booking_id,omitempty"`
}

// FetchUserRides returns every ride the caller touches — as host or as
// an accepted/pending/rejected passenger — annotated with viewer
// state so the Trips screen can bucket + style each row without
// asking "wait, what's my relationship to this ride?" client-side.
//
// Perf:
//   - One query for hosted rides, one for booked rides — both bounded
//     by user-scoped indexes.
//   - Booking + ride join replaced with a LEFT JOIN inline so we
//     don't loop fetching bookings per ride.
//   - Each ride is touched O(1) by the viewer-state resolver: we
//     pass it the (already loaded) booking row for that ride.
func FetchUserRides(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated or not found"})
	}
	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid user data"})
	}
	viewerID := user.ID

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1) Every ride the user has any relationship with — hosted OR
	//    booked (any status). Single query with LEFT JOIN, viewer-id
	//    indexed on both sides.
	type row struct {
		RideID        uuid.UUID
		HostUserID    uuid.UUID
		StartLocation string
		EndLocation   string
		StartTime     time.Time
		TotalSeats    uint
		BookedSeats   uint
		TotalPrice    uint
		IsOngoing     uint
		IsSameGender  uint
		BookingID     *uuid.UUID
		PassengerID   *uuid.UUID
		RequestStatus *string
	}
	var rows []row
	if err := database.Database.Db.WithContext(ctx).
		Table("rides AS r").
		Select(`
			r.id            AS ride_id,
			r.host_user_id,
			r.start_location,
			r.end_location,
			r.start_time,
			r.total_seats,
			r.booked_seats,
			r.total_price,
			r.is_ongoing,
			r.is_same_gender,
			b.id            AS booking_id,
			b.passenger_id,
			b.request_status
		`).
		Joins("LEFT JOIN bookings b ON b.ride_id = r.id AND b.passenger_id = ?", viewerID).
		Where("r.host_user_id = ? OR b.passenger_id = ?", viewerID, viewerID).
		Order("r.start_time DESC").
		Scan(&rows).Error; err != nil {
		log.Printf("FetchUserRides query failed: %v", err)
		return c.Status(fiber.StatusBadGateway).SendString("Error finding rides for user")
	}

	out := make([]UserRidesResponse, 0, len(rows))
	for _, r := range rows {
		// Reconstruct a partial Ride/Booking pair to feed the
		// state resolver — zero extra DB hits.
		ride := models.Ride{
			HostUserID:  r.HostUserID,
			StartTime:   r.StartTime,
			TotalSeats:  r.TotalSeats,
			BookedSeats: r.BookedSeats,
			IsOngoing:   r.IsOngoing,
		}
		var viewerBooking *models.Booking
		if r.BookingID != nil && r.RequestStatus != nil {
			viewerBooking = &models.Booking{
				BaseModel:     models.BaseModel{ID: *r.BookingID},
				RideID:        r.RideID,
				PassengerID:   *r.PassengerID,
				RequestStatus: *r.RequestStatus,
			}
		}
		viewerCtx := rides.ResolveViewerState(&ride, viewerID, viewerBooking)

		entry := UserRidesResponse{
			RideID:          r.RideID,
			HostUserID:      r.HostUserID,
			StartLocation:   r.StartLocation,
			EndLocation:     r.EndLocation,
			StartTime:       r.StartTime,
			TotalSeats:      r.TotalSeats,
			BookedSeats:     r.BookedSeats,
			TotalPrice:      r.TotalPrice,
			IsOngoing:       r.IsOngoing,
			IsSameGender:    r.IsSameGender,
			PassengerID:     r.PassengerID,
			IsUserHost:      r.HostUserID == viewerID,
			ViewerState:     viewerCtx.State,
			Actions:         viewerCtx.Actions,
			ViewerBookingID: viewerCtx.BookingID,
		}
		if r.RequestStatus != nil {
			entry.RequestStatus = *r.RequestStatus
		}
		out = append(out, entry)
	}

	return c.Status(fiber.StatusOK).JSON(out)
}
