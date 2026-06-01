package rides

import (
	"log"
	"sync"
	"time"
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
	// Extra surfaces consumed by the passenger profile sheet on the
	// host's Ride Management screen. UPI VPA powers the "Pay" pill,
	// is_email_verified drives the verified checkmark, institute lets
	// the host see "VIT" / "Stanford" at a glance.
	PassengerUPIVPA        string `json:"passenger_upi_vpa,omitempty"`
	PassengerIsVerified    bool   `json:"passenger_is_verified"`
	PassengerInstituteName string `json:"passenger_institute_name,omitempty"`
}

type RideDetailsComplete struct {
	ID                        string  `json:"id"`
	HostUserID                string  `json:"host_user_id"`
	HostUserName              string  `json:"host_user_name"`
	HostIsVerified            bool    `json:"host_is_verified"`
	HostInstituteName         *string `json:"host_institute_name,omitempty"`
	HostSameInstituteAsViewer bool    `json:"host_same_institute_as_viewer"`
	// Vehicle ID surfaced *only* to host + confirmed passengers
	// (the pickup audience). Other viewers get an empty string so
	// random rides on the map don't leak the plate.
	VehicleInfo    string   `json:"vehicle_info,omitempty"`
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
//   - Host + ride loaded in a single projected query.
//   - Bookings + passenger details loaded in one projected query (was
//     N+1 passenger fetches, then a separate passenger batch).
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

	type rideHostRow struct {
		ID                uuid.UUID  `gorm:"column:id"`
		HostUserID        uuid.UUID  `gorm:"column:host_user_id"`
		StartLocation     string     `gorm:"column:start_location"`
		EndLocation       string     `gorm:"column:end_location"`
		StartLatitude     *float64   `gorm:"column:start_latitude"`
		StartLongitude    *float64   `gorm:"column:start_longitude"`
		EndLatitude       *float64   `gorm:"column:end_latitude"`
		EndLongitude      *float64   `gorm:"column:end_longitude"`
		StartTime         time.Time  `gorm:"column:start_time"`
		TotalPrice        uint       `gorm:"column:total_price"`
		TotalSeats        uint       `gorm:"column:total_seats"`
		BookedSeats       uint       `gorm:"column:booked_seats"`
		IsOngoing         uint       `gorm:"column:is_ongoing"`
		IsSameGender      uint       `gorm:"column:is_same_gender"`
		VehicleInfo       string     `gorm:"column:vehicle_info"`
		CreatedAt         time.Time  `gorm:"column:created_at"`
		HostID            uuid.UUID  `gorm:"column:host_id"`
		HostName          string     `gorm:"column:host_name"`
		HostEmail         string     `gorm:"column:host_email"`
		HostProfilePic    string     `gorm:"column:host_profile_picture_url"`
		HostContactNumber string     `gorm:"column:host_contact_number"`
		HostInstituteID   *uuid.UUID `gorm:"column:host_institute_id"`
		HostVerified      bool       `gorm:"column:host_is_email_verified"`
		HostInstituteName *string    `gorm:"column:host_institute_name"`
	}

	type bookingPassengerRow struct {
		ID                         uuid.UUID `gorm:"column:id"`
		RideID                     uuid.UUID `gorm:"column:ride_id"`
		PassengerID                uuid.UUID `gorm:"column:passenger_id"`
		RequestStatus              string    `gorm:"column:request_status"`
		CreatedAt                  time.Time `gorm:"column:created_at"`
		PassengerFound             bool      `gorm:"column:passenger_found"`
		PassengerName              string    `gorm:"column:passenger_name"`
		PassengerEmail             string    `gorm:"column:passenger_email"`
		PassengerProfilePictureURL string    `gorm:"column:passenger_profile_picture_url"`
		PassengerContactNumber     string    `gorm:"column:passenger_contact_number"`
		PassengerUPIVPA            string    `gorm:"column:passenger_upi_vpa"`
		PassengerIsVerified        bool      `gorm:"column:passenger_is_verified"`
		PassengerInstituteName     *string   `gorm:"column:passenger_institute_name"`
	}

	// Keep the detail-open path to two parallel queries: ride+host and
	// bookings+passengers. Explicit projections avoid unused model hydration.
	var (
		rideHost    rideHostRow
		bookingRows []bookingPassengerRow
		rideErr     error
		bookingsErr error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		rideErr = database.Database.Db.
			Table("rides AS r").
			Select(`
				r.id,
				r.host_user_id,
				r.start_location,
				r.end_location,
				r.start_latitude,
				r.start_longitude,
				r.end_latitude,
				r.end_longitude,
				r.start_time,
				r.total_price,
				r.total_seats,
				r.booked_seats,
				r.is_ongoing,
				r.is_same_gender,
				r.vehicle_info,
				r.created_at,
				u.id                  AS host_id,
				u.name                AS host_name,
				u.email               AS host_email,
				u.profile_picture_url AS host_profile_picture_url,
				u.contact_number      AS host_contact_number,
				u.institute_id        AS host_institute_id,
				u.is_email_verified   AS host_is_email_verified,
				i.name                AS host_institute_name
			`).
			Joins("LEFT JOIN users u ON u.id = r.host_user_id").
			Joins("LEFT JOIN institutes i ON i.id = u.institute_id").
			Where("r.id = ? AND r.deleted_at IS NULL", rideID).
			Take(&rideHost).Error
	}()
	go func() {
		defer wg.Done()
		bookingsErr = database.Database.Db.
			Table("bookings AS b").
			Select(`
				b.id,
				b.ride_id,
				b.passenger_id,
				b.request_status,
				b.created_at,
				(u.id IS NOT NULL)                         AS passenger_found,
				COALESCE(u.name, '')                       AS passenger_name,
				COALESCE(u.email, '')                      AS passenger_email,
				COALESCE(u.profile_picture_url, '')        AS passenger_profile_picture_url,
				COALESCE(u.contact_number, '')             AS passenger_contact_number,
				COALESCE(u.upi_vpa, '')                    AS passenger_upi_vpa,
				COALESCE(u.is_email_verified, FALSE)       AS passenger_is_verified,
				i.name                                     AS passenger_institute_name
			`).
			Joins("LEFT JOIN users u ON u.id = b.passenger_id").
			Joins("LEFT JOIN institutes i ON i.id = u.institute_id").
			Where("b.ride_id = ? AND b.deleted_at IS NULL", rideID).
			Order("b.created_at ASC").
			Scan(&bookingRows).Error
	}()
	wg.Wait()
	if rideErr != nil {
		log.Printf("ride lookup failed for %s: %v", rideID, rideErr)
		return c.Status(404).JSON(fiber.Map{"error": "Ride not found"})
	}
	if bookingsErr != nil {
		log.Printf("bookings lookup failed for ride %s: %v", rideID, bookingsErr)
		return c.Status(500).JSON(fiber.Map{"error": "Error fetching bookings"})
	}
	if rideHost.HostID == (uuid.UUID{}) {
		log.Printf("host lookup failed for ride %s: host %s not found", rideID, rideHost.HostUserID)
		return c.Status(500).JSON(fiber.Map{"error": "Host details not found"})
	}

	ride := models.Ride{
		BaseModel: models.BaseModel{
			ID:        rideHost.ID,
			CreatedAt: rideHost.CreatedAt,
		},
		HostUserID:     rideHost.HostUserID,
		StartLocation:  rideHost.StartLocation,
		EndLocation:    rideHost.EndLocation,
		StartLatitude:  rideHost.StartLatitude,
		StartLongitude: rideHost.StartLongitude,
		EndLatitude:    rideHost.EndLatitude,
		EndLongitude:   rideHost.EndLongitude,
		StartTime:      rideHost.StartTime,
		TotalPrice:     rideHost.TotalPrice,
		TotalSeats:     rideHost.TotalSeats,
		BookedSeats:    rideHost.BookedSeats,
		IsOngoing:      rideHost.IsOngoing,
		IsSameGender:   rideHost.IsSameGender,
		VehicleInfo:    rideHost.VehicleInfo,
	}
	host := models.User{
		BaseModel:         models.BaseModel{ID: rideHost.HostID},
		Name:              rideHost.HostName,
		Email:             rideHost.HostEmail,
		ProfilePictureURL: rideHost.HostProfilePic,
		ContactNumber:     rideHost.HostContactNumber,
		InstituteID:       rideHost.HostInstituteID,
		IsEmailVerified:   rideHost.HostVerified,
	}
	hostInstituteName := rideHost.HostInstituteName

	// Same-institute flag — drives the "Same campus" chip on the
	// client. Only true if both viewer and host have a non-nil
	// institute and they match.
	hostSameInstituteAsViewer := host.InstituteID != nil &&
		user.InstituteID != nil &&
		*host.InstituteID == *user.InstituteID

	bookingDetails := make([]BookingDetail, 0, len(bookingRows))
	bookedSeats := 0
	var viewerBooking *models.Booking
	isViewerHost := user.ID == ride.HostUserID
	for i := range bookingRows {
		row := bookingRows[i]
		if row.PassengerID == ride.HostUserID {
			continue // host doesn't appear in the bookings list
		}
		booking := models.Booking{
			BaseModel: models.BaseModel{
				ID:        row.ID,
				CreatedAt: row.CreatedAt,
			},
			RideID:        row.RideID,
			PassengerID:   row.PassengerID,
			RequestStatus: row.RequestStatus,
		}
		if row.PassengerID == user.ID {
			bk := booking
			viewerBooking = &bk
		}
		if row.RequestStatus == "accepted" {
			bookedSeats++
		}
		// Booking passenger details include email/contact/UPI. Only
		// the host gets the full management list; non-host viewers get
		// their own booking row only so viewer_state hydration still
		// works without leaking other passengers' PII.
		if !isViewerHost && row.PassengerID != user.ID {
			continue
		}
		if !row.PassengerFound {
			// Couldn't load this passenger — skip the row rather
			// than emit half-empty data.
			continue
		}
		passengerInstituteName := ""
		if row.PassengerInstituteName != nil {
			passengerInstituteName = *row.PassengerInstituteName
		}
		bookingDetails = append(bookingDetails, BookingDetail{
			ID:                         row.ID.String(),
			PassengerID:                row.PassengerID.String(),
			RequestStatus:              row.RequestStatus,
			CreatedAt:                  row.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			PassengerName:              row.PassengerName,
			PassengerEmail:             row.PassengerEmail,
			PassengerProfilePictureURL: row.PassengerProfilePictureURL,
			PassengerContactNumber:     row.PassengerContactNumber,
			PassengerUPIVPA:            row.PassengerUPIVPA,
			PassengerIsVerified:        row.PassengerIsVerified,
			PassengerInstituteName:     passengerInstituteName,
		})
	}

	// Use the live accepted-booking count we just computed instead of
	// trusting rides.booked_seats blindly; legacy rows can drift, but
	// the detail page already has the authoritative bookings slice.
	rideForViewer := ride
	rideForViewer.BookedSeats = uint(bookedSeats)
	viewerCtx := ResolveViewerState(&rideForViewer, user.ID, viewerBooking)
	if viewerCtx.State == StatePast {
		pendingRatingSet, err := BuildPendingRatingSet(user.ID)
		if err != nil {
			log.Printf("rating prompt lookup failed for user %s ride %s: %v", user.ID, ride.ID, err)
		} else {
			viewerCtx.Actions.CanRate = pendingRatingSet[ride.ID]
		}
	}

	// Vehicle info only shown to host + confirmed passengers — the
	// audience that's actually meeting at the pickup point. Random
	// viewers see an empty string so the plate isn't broadcast.
	vehicleInfoForViewer := ""
	if viewerCtx.State == StateHost || viewerCtx.State == StateConfirmedPassenger {
		vehicleInfoForViewer = ride.VehicleInfo
	}

	response := RideDetailsComplete{
		ID:                        ride.ID.String(),
		HostUserID:                ride.HostUserID.String(),
		HostUserName:              host.Name,
		HostIsVerified:            host.IsEmailVerified,
		HostInstituteName:         hostInstituteName,
		HostSameInstituteAsViewer: hostSameInstituteAsViewer,
		VehicleInfo:               vehicleInfoForViewer,
		StartLocation:             ride.StartLocation,
		EndLocation:               ride.EndLocation,
		StartLatitude:             ride.StartLatitude,
		StartLongitude:            ride.StartLongitude,
		EndLatitude:               ride.EndLatitude,
		EndLongitude:              ride.EndLongitude,
		StartTime:                 ride.StartTime.Format("2006-01-02T15:04:05Z07:00"),
		TotalPrice:                int(ride.TotalPrice),
		TotalSeats:                int(ride.TotalSeats),
		BookedSeats:               bookedSeats,
		IsOngoing:                 ride.IsOngoing > 0,
		CreatedAt:                 ride.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		IsUserHost:                user.ID == ride.HostUserID,
		ViewerState:               viewerCtx.State,
		Actions:                   viewerCtx.Actions,
		ViewerBookingID:           viewerCtx.BookingID,
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
