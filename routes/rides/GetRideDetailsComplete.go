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
	
	if err := database.Database.Db.Where("id = ?", rideID).First(&ride).Error; err != nil {
		log.Printf("Error finding ride for ID %s: %v\n", rideID, err)
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	if err := database.Database.Db.
		Select("id, name, email, profile_picture_url, contact_number").
		Where("id = ?", ride.HostUserID).
		First(&host).Error; err != nil {
		log.Printf("Error finding host with ID %s: %v\n", ride.HostUserID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Host details not found",
		})
	}

	var bookings []models.Booking
	if err := database.Database.Db.
		Where("ride_id = ?", rideID).
		Order("created_at ASC").
		Find(&bookings).Error; err != nil {
		log.Printf("Error fetching bookings for ride %s: %v\n", rideID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error fetching bookings",
		})
	}

	var bookingDetails []BookingDetail
	for _, booking := range bookings {
		var passenger models.User
		if err := database.Database.Db.
			Select("id, name, email, profile_picture_url, contact_number").
			Where("id = ?", booking.PassengerID).
			First(&passenger).Error; err != nil {
			log.Printf("Warning: Could not fetch passenger details for booking %s: %v\n", booking.ID, err)
			continue
		}

		bookingDetails = append(bookingDetails, BookingDetail{
			ID:            booking.ID.String(),
			PassengerID:   booking.PassengerID.String(),
			RequestStatus: booking.RequestStatus,
			CreatedAt:     booking.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Passenger: PassengerDetail{
				ID:                passenger.ID.String(),
				Name:              passenger.Name,
				Email:             passenger.Email,
				ProfilePictureURL: passenger.ProfilePictureURL,
				ContactNumber:     passenger.ContactNumber,
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
