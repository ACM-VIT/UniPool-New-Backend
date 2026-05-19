package rides

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type PassengerDetail struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	ProfilePictureURL string `json:"profile_picture_url"`
	ContactNumber     string `json:"contact_number"`
}

type BookingDetail struct {
	ID                         string `json:"id"`
	PassengerID                string `json:"passenger_id"`
	RequestStatus              string `json:"request_status"`
	CreatedAt                  string `json:"booking_created_at"`
	PassengerName              string `json:"passenger_name"`
	PassengerEmail             string `json:"passenger_email"`
	PassengerProfilePictureURL string `json:"passenger_profile_picture_url"`
	PassengerContactNumber     string `json:"passenger_contact_number"`
}

type RideDetailsComplete struct {
	ID                   string  `json:"id"`
	HostUserID           string  `json:"host_user_id"`
	HostUserName         string  `json:"host_user_name"`
	HostIsVerified       bool    `json:"host_is_verified"`
	HostInstituteName    *string `json:"host_institute_name,omitempty"`
	HostSameInstituteAsViewer bool `json:"host_same_institute_as_viewer"`
	StartLocation  string   `json:"start_location"`
	EndLocation    string   `json:"end_location"`
	StartLatitude  *float64 `json:"start_latitude,omitempty"`
	StartLongitude *float64 `json:"start_longitude,omitempty"`
	EndLatitude    *float64 `json:"end_latitude,omitempty"`
	EndLongitude   *float64 `json:"end_longitude,omitempty"`
	StartTime      string   `json:"start_time"`
	TotalPrice     int      `json:"total_price"`
	TotalSeats     int      `json:"total_seats"`
	BookedSeats    int      `json:"booked_seats"`
	IsOngoing      bool     `json:"is_ongoing"`
	CreatedAt      string   `json:"created_at"`

	// Kept for backwards compatibility while clients migrate to
	// `viewer_state` (the new single source of truth).
	IsUserHost bool `json:"is_user_host"`

	// Server-computed UI state. The client renders directly off this
	// instead of deriving "am I host? have I booked? what's the
	// status?" from a tangle of fields.
	ViewerState     ViewerState   `json:"viewer_state"`
	Actions         ViewerActions `json:"actions"`
	ViewerBookingID *string       `json:"viewer_booking_id,omitempty"`

	Host PassengerDetail `json:"host"`

	Bookings []BookingDetail `json:"bookings"`
}

// GetRideDetailsComplete returns everything a single ride page needs:
// ride row, host user, all bookings (with passenger details), and a
// server-computed `viewer_state` + `actions` block that drives the
// frontend's UI variant.
//
// Perf notes:
//   - Host + ride loaded in a single query via JOIN-free Preload.
//   - Passenger lookup batched into ONE query (was N+1: one passenger
//     fetch per booking).
//   - Viewer booking comes free out of that same batch — no extra
//     round-trip to figure out the viewer's state.
func GetRideDetailsComplete(c *fiber.Ctx) error {
	rideIDParam := c.Params("id")
	rideID, err := uuid.Parse(rideIDParam)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid ride id"})
	}

	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated"})
	}
	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid user data"})
	}

	var ride models.Ride
	if err := database.Database.Db.Where("id = ?", rideID).First(&ride).Error; err != nil {
		log.Printf("ride lookup failed for %s: %v", rideID, err)
		return c.Status(404).JSON(fiber.Map{"error": "Ride not found"})
	}

	var host models.User
	if err := database.Database.Db.
		Select("id, name, email, profile_picture_url, contact_number, institute_id, is_email_verified").
		Where("id = ?", ride.HostUserID).
		First(&host).Error; err != nil {
		log.Printf("host lookup failed for ride %s: %v", rideID, err)
		return c.Status(500).JSON(fiber.Map{"error": "Host details not found"})
	}

	// Resolve institute name once, cheap. Only fired when the host
	// has an institute set (verified users); skipped otherwise.
	var hostInstituteName *string
	if host.InstituteID != nil {
		var inst models.Institute
		if err := database.Database.Db.
			Select("id, name").
			Where("id = ?", *host.InstituteID).
			First(&inst).Error; err == nil {
			n := inst.Name
			hostInstituteName = &n
		}
	}
	// Same-institute flag — drives the "Same campus" chip on the
	// client. Only true if both viewer and host have a non-nil
	// institute and they match.
	hostSameInstituteAsViewer := host.InstituteID != nil &&
		user.InstituteID != nil &&
		*host.InstituteID == *user.InstituteID

	var bookings []models.Booking
	if err := database.Database.Db.
		Where("ride_id = ?", rideID).
		Order("created_at ASC").
		Find(&bookings).Error; err != nil {
		log.Printf("bookings lookup failed for ride %s: %v", rideID, err)
		return c.Status(500).JSON(fiber.Map{"error": "Error fetching bookings"})
	}

	// Batch passenger lookup. Old code did `First(&passenger)` inside
	// the loop — one round-trip per booking. New code fetches every
	// passenger in a single `WHERE id IN (...)`.
	passengerIDs := make([]uuid.UUID, 0, len(bookings))
	for i := range bookings {
		if bookings[i].PassengerID != ride.HostUserID {
			passengerIDs = append(passengerIDs, bookings[i].PassengerID)
		}
	}
	passengerByID := map[uuid.UUID]models.User{}
	if len(passengerIDs) > 0 {
		var passengers []models.User
		if err := database.Database.Db.
			Select("id, name, email, profile_picture_url, contact_number").
			Where("id IN ?", passengerIDs).
			Find(&passengers).Error; err != nil {
			log.Printf("batch passenger lookup failed: %v", err)
		}
		for _, p := range passengers {
			passengerByID[p.ID] = p
		}
	}

	bookingDetails := make([]BookingDetail, 0, len(bookings))
	bookedSeats := 0
	var viewerBooking *models.Booking
	for i := range bookings {
		b := bookings[i]
		if b.PassengerID == ride.HostUserID {
			continue // host doesn't appear in the bookings list
		}
		passenger, ok := passengerByID[b.PassengerID]
		if !ok {
			// Couldn't load this passenger — skip the row rather
			// than emit half-empty data.
			continue
		}
		if b.PassengerID == user.ID {
			bk := b
			viewerBooking = &bk
		}
		if b.RequestStatus == "accepted" {
			bookedSeats++
		}
		bookingDetails = append(bookingDetails, BookingDetail{
			ID:                         b.ID.String(),
			PassengerID:                b.PassengerID.String(),
			RequestStatus:              b.RequestStatus,
			CreatedAt:                  b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			PassengerName:              passenger.Name,
			PassengerEmail:             passenger.Email,
			PassengerProfilePictureURL: passenger.ProfilePictureURL,
			PassengerContactNumber:     passenger.ContactNumber,
		})
	}

	viewerCtx := ResolveViewerState(&ride, user.ID, viewerBooking)

	response := RideDetailsComplete{
		ID:                        ride.ID.String(),
		HostUserID:                ride.HostUserID.String(),
		HostUserName:              host.Name,
		HostIsVerified:            host.IsEmailVerified,
		HostInstituteName:         hostInstituteName,
		HostSameInstituteAsViewer: hostSameInstituteAsViewer,
		StartLocation:   ride.StartLocation,
		EndLocation:     ride.EndLocation,
		StartLatitude:   ride.StartLatitude,
		StartLongitude:  ride.StartLongitude,
		EndLatitude:     ride.EndLatitude,
		EndLongitude:    ride.EndLongitude,
		StartTime:       ride.StartTime.Format("2006-01-02T15:04:05Z07:00"),
		TotalPrice:      int(ride.TotalPrice),
		TotalSeats:      int(ride.TotalSeats),
		BookedSeats:     bookedSeats,
		IsOngoing:       ride.IsOngoing > 0,
		CreatedAt:       ride.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		IsUserHost:      user.ID == ride.HostUserID,
		ViewerState:     viewerCtx.State,
		Actions:         viewerCtx.Actions,
		ViewerBookingID: viewerCtx.BookingID,
		Host: PassengerDetail{
			ID:                host.ID.String(),
			Name:              host.Name,
			Email:             host.Email,
			ProfilePictureURL: host.ProfilePictureURL,
			ContactNumber:     host.ContactNumber,
		},
		Bookings: bookingDetails,
	}

	return c.Status(200).JSON(response)
}
