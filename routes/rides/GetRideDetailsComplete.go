package rides

import (
	"log"
	"sync"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
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

	// Three sequential DB round-trips collapsed to two parallel ones.
	// The ride lookup gives us host_user_id and is the only blocker
	// for the host+institute follow-up; bookings only need rideID
	// (which we have from the URL param) so it fires in parallel
	// with the ride lookup. Then host+institute is one chained pull
	// (host needs to land before we know its institute_id).
	//
	// Wall time was 3-4 sequential round-trips (~75-100ms); now it
	// peaks at two phases (~50ms) for the typical "viewer has not
	// signed in to a verified institute" case and ~75ms when the
	// host institute lookup is needed.
	var (
		ride        models.Ride
		bookings    []models.Booking
		rideErr     error
		bookingsErr error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		rideErr = database.Database.Db.
			Select("id, host_user_id, start_location, end_location, start_latitude, start_longitude, end_latitude, end_longitude, start_time, total_price, total_seats, booked_seats, is_ongoing, is_same_gender, vehicle_info, created_at, updated_at").
			Where("id = ?", rideID).
			First(&ride).Error
	}()
	go func() {
		defer wg.Done()
		bookingsErr = database.Database.Db.
			Where("ride_id = ?", rideID).
			Order("created_at ASC").
			Find(&bookings).Error
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

	// Host + institute name in ONE query via LEFT JOIN — the previous
	// two-step (load host, then conditionally load institute) was
	// two sequential round-trips for every verified host. Wrapping
	// institute fields in a NULL-tolerant projection lets us collapse
	// both cases.
	type hostRow struct {
		ID                uuid.UUID  `gorm:"column:id"`
		Name              string     `gorm:"column:name"`
		Email             string     `gorm:"column:email"`
		ProfilePictureURL string     `gorm:"column:profile_picture_url"`
		ContactNumber     string     `gorm:"column:contact_number"`
		InstituteID       *uuid.UUID `gorm:"column:institute_id"`
		IsEmailVerified   bool       `gorm:"column:is_email_verified"`
		InstituteName     *string    `gorm:"column:institute_name"`
	}
	var hr hostRow
	if err := database.Database.Db.
		Table("users AS u").
		Select(`u.id, u.name, u.email, u.profile_picture_url, u.contact_number,
		        u.institute_id, u.is_email_verified, i.name AS institute_name`).
		Joins("LEFT JOIN institutes i ON i.id = u.institute_id").
		Where("u.id = ?", ride.HostUserID).
		Scan(&hr).Error; err != nil || hr.ID == (uuid.UUID{}) {
		log.Printf("host lookup failed for ride %s: %v", rideID, err)
		return c.Status(500).JSON(fiber.Map{"error": "Host details not found"})
	}
	host := models.User{
		BaseModel:         models.BaseModel{ID: hr.ID},
		Name:              hr.Name,
		Email:             hr.Email,
		ProfilePictureURL: hr.ProfilePictureURL,
		ContactNumber:     hr.ContactNumber,
		InstituteID:       hr.InstituteID,
		IsEmailVerified:   hr.IsEmailVerified,
	}
	hostInstituteName := hr.InstituteName

	// Same-institute flag — drives the "Same campus" chip on the
	// client. Only true if both viewer and host have a non-nil
	// institute and they match.
	hostSameInstituteAsViewer := host.InstituteID != nil &&
		user.InstituteID != nil &&
		*host.InstituteID == *user.InstituteID

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
			Preload("Institute", func(db *gorm.DB) *gorm.DB {
				// Only Name is read at line ~196; pulling the full
				// Institute row (Country, timestamps) is wasted bytes
				// across every passenger.
				return db.Select("id, name")
			}).
			Select("id, name, email, profile_picture_url, contact_number, upi_vpa, is_email_verified, institute_id").
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
	isViewerHost := user.ID == ride.HostUserID
	for i := range bookings {
		b := bookings[i]
		if b.PassengerID == ride.HostUserID {
			continue // host doesn't appear in the bookings list
		}
		if b.PassengerID == user.ID {
			bk := b
			viewerBooking = &bk
		}
		if b.RequestStatus == "accepted" {
			bookedSeats++
		}
		// Booking passenger details include email/contact/UPI. Only
		// the host gets the full management list; non-host viewers get
		// their own booking row only so viewer_state hydration still
		// works without leaking other passengers' PII.
		if !isViewerHost && b.PassengerID != user.ID {
			continue
		}
		passenger, ok := passengerByID[b.PassengerID]
		if !ok {
			// Couldn't load this passenger — skip the row rather
			// than emit half-empty data.
			continue
		}
		passengerInstituteName := ""
		if passenger.Institute != nil {
			passengerInstituteName = passenger.Institute.Name
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
			PassengerUPIVPA:            passenger.UPIVPA,
			PassengerIsVerified:        passenger.IsEmailVerified,
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
