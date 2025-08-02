package rides

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

type PassengerDetail struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	ProfilePictureURL string `json:"profile_picture_url"`
	ContactNumber     string `json:"contact_number"`
}

type BookingDetail struct {
	ID            string          `json:"id"`
	PassengerID   string          `json:"passenger_id"`
	RequestStatus string          `json:"request_status"`
	CreatedAt     string          `json:"created_at"`
	Passenger     PassengerDetail `json:"passenger"`
}

type RideDetailsComplete struct {
	ID            string `json:"id"`
	HostUserID    string `json:"host_user_id"`
	HostUserName  string `json:"host_user_name"`
	StartLocation string `json:"start_location"`
	EndLocation   string `json:"end_location"`
	StartTime     string `json:"start_time"`
	TotalPrice    int    `json:"total_price"`
	TotalSeats    int    `json:"total_seats"`
	BookedSeats   int    `json:"booked_seats"`
	IsOngoing     bool   `json:"is_ongoing"`
	CreatedAt     string `json:"created_at"`

	IsUserHost bool `json:"is_user_host"`

	Host PassengerDetail `json:"host"`

	Bookings []BookingDetail `json:"bookings"`
}

func GetRideDetailsComplete(c *fiber.Ctx) error {
	rideID := c.Params("id")
	
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"error": "Invalid user data",
		})
	}

	var ride models.Ride
	var host models.User
	
	if err := database.Database.Db.
		Select("rides.*, users.name as host_name, users.email as host_email, users.profile_picture_url as host_profile_picture_url, users.contact_number as host_contact_number").
		Table("rides").
		Joins("JOIN users ON users.id = rides.host_user_id").
		Where("rides.id = ?", rideID).
		First(&ride).Error; err != nil {
		log.Printf("Error finding ride with host details for ID %s: %v\n", rideID, err)
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	if err := database.Database.Db.
		Select("id, name, email, profile_picture_url, contact_number").
		First(&host, "id = ?", ride.HostUserID).Error; err != nil {
		log.Printf("Error finding host with ID %s: %v\n", ride.HostUserID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Host details not found",
		})
	}

	type BookingWithPassenger struct {
		BookingID     string `json:"booking_id"`
		PassengerID   string `json:"passenger_id"`
		RequestStatus string `json:"request_status"`
		BookingCreatedAt string `json:"booking_created_at"`
		
		PassengerName              string `json:"passenger_name"`
		PassengerEmail             string `json:"passenger_email"`
		PassengerProfilePictureURL string `json:"passenger_profile_picture_url"`
		PassengerContactNumber     string `json:"passenger_contact_number"`
	}

	var bookingsWithPassengers []BookingWithPassenger
	
	if err := database.Database.Db.
		Table("bookings").
		Select(`
			bookings.id as booking_id,
			bookings.passenger_id,
			bookings.request_status,
			bookings.created_at as booking_created_at,
			users.name as passenger_name,
			users.email as passenger_email,
			users.profile_picture_url as passenger_profile_picture_url,
			users.contact_number as passenger_contact_number
		`).
		Joins("JOIN users ON users.id = bookings.passenger_id").
		Where("bookings.ride_id = ?", rideID).
		Order("bookings.created_at ASC").
		Find(&bookingsWithPassengers).Error; err != nil {
		log.Printf("Error fetching bookings with passengers for ride %s: %v\n", rideID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error fetching bookings",
		})
	}

	var bookingDetails []BookingDetail
	for _, bwp := range bookingsWithPassengers {
		bookingDetails = append(bookingDetails, BookingDetail{
			ID:            bwp.BookingID,
			PassengerID:   bwp.PassengerID,
			RequestStatus: bwp.RequestStatus,
			CreatedAt:     bwp.BookingCreatedAt,
			Passenger: PassengerDetail{
				ID:                bwp.PassengerID,
				Name:              bwp.PassengerName,
				Email:             bwp.PassengerEmail,
				ProfilePictureURL: bwp.PassengerProfilePictureURL,
				ContactNumber:     bwp.PassengerContactNumber,
			},
		})
	}

	hostHasBooking := false
	for _, booking := range bookingDetails {
		if booking.PassengerID == ride.HostUserID.String() {
			hostHasBooking = true
			break
		}
	}

	if !hostHasBooking {
		hostBooking := BookingDetail{
			ID:            "host-booking",
			PassengerID:   ride.HostUserID.String(),
			RequestStatus: "accepted",
			CreatedAt:     ride.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Passenger: PassengerDetail{
				ID:                host.ID.String(),
				Name:              host.Name,
				Email:             host.Email,
				ProfilePictureURL: host.ProfilePictureURL,
				ContactNumber:     host.ContactNumber,
			},
		}
		bookingDetails = append([]BookingDetail{hostBooking}, bookingDetails...)
	}

	bookedSeats := 0
	for _, booking := range bookingDetails {
		if booking.RequestStatus == "accepted" && booking.ID != "host-booking" {
			bookedSeats++
		}
	}

	response := RideDetailsComplete{
		ID:            ride.ID.String(),
		HostUserID:    ride.HostUserID.String(),
		HostUserName:  host.Name,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime.Format("2006-01-02T15:04:05Z07:00"),
		TotalPrice:    int(ride.TotalPrice),
		TotalSeats:    int(ride.TotalSeats),
		BookedSeats:   bookedSeats,
		IsOngoing:     ride.IsOngoing > 0,
		CreatedAt:     ride.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		IsUserHost:    user.ID == ride.HostUserID,
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
