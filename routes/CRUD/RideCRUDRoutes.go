package CRUD

import (
	"log"
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Ride response model
type RideResponse struct {
	RideID        uuid.UUID `json:"id"`
	HostUserID    uuid.UUID `json:"host_user_id"`
	HostUserName  string    `json:"host_user_name"`
	StartLocation string    `json:"start_location"`
	EndLocation   string    `json:"end_location"`
	StartTime     time.Time `json:"start_time"`
	TotalSeats    uint      `json:"total_seats"`
	BookedSeats   uint      `json:"booked_seats"`
	TotalPrice    uint      `json:"total_price"`
	IsOngoing     uint      `json:"is_ongoing"`
	IsSameGender  uint      `json:"is_same_gender"`
}

// Function to create a ride
func CreateRide(c *fiber.Ctx) error {
	var ride models.Ride

	   err := c.BodyParser(&ride)
	   if err != nil {
			   log.Printf("Error parsing JSON: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error parsing JSON (body parser)",
			   })
	   }

	//Logs a 400 error if the Ride struct is invalid
	   err = helpers.ValidateRide(ride)
	   if err != nil {
			   log.Printf("Error validating ride: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error validating ride - (helper function)",
			   })
	   }

	// Checks if the host user ID exists
	   var hostUser models.User
	   result := database.Database.Db.First(&hostUser, ride.HostUserID)
	   if result.Error == gorm.ErrRecordNotFound {
			   log.Printf("Host user ID does not exist")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Host user ID does not exist",
			   })
	   } else if result.Error != nil {
			   log.Printf("Error finding host user: %v\n", result.Error)
			   return c.Status(502).JSON(fiber.Map{
					   "error": "Error finding host user",
			   })
	   }

	// Checks if start time is in the future
	   if ride.StartTime.Before(time.Now()) {
			   log.Printf("Start time is in the past")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Start time is in the past",
			   })
	   }

	// Checks if the host user has enough seats
	   if ride.TotalSeats <= ride.BookedSeats {
			   log.Printf("Total seats available should be more than booked seats")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Total seats available should be more than booked seats",
			   })
	   }

	if ride.IsSameGender == 1 {
		log.Printf("This ride has been created for the same gender only.")
	} else if ride.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	   if ride.TotalPrice < 25 || ride.TotalPrice > 10000 {
			   log.Printf("Price too low")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Price too low!",
			   })
	   }

	// Create the ride in the database
	   result = database.Database.Db.Create(&ride)

	   if result.Error != nil {
			   log.Printf("Error creating ride: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error creating ride - (database creation error)",
			   })
	   }

	// Create the ride response
	rideResponse := RideResponse{
		RideID:        ride.ID,
		HostUserID:    ride.HostUserID,
		HostUserName:  hostUser.Name,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		TotalSeats:    ride.TotalSeats,
		BookedSeats:   ride.BookedSeats,
		TotalPrice:    ride.TotalPrice,
		IsOngoing:     ride.IsOngoing,
		IsSameGender:  ride.IsSameGender,
	}

	log.Printf("Ride with id %v created\n", ride.ID)
	return c.Status(200).JSON(rideResponse)
}

// Function to get all rides
func GetRides(c *fiber.Ctx) error {
	var rides []models.Ride
	result := database.Database.Db.Find(&rides)

	   if result.Error != nil {
			   log.Printf("Error getting rides: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error getting rides",
			   })
	   }

	ridesResponse := make([]RideResponse, len(rides))

	for i, ride := range rides {
		// Find the host user
		var hostUser models.User
		result := database.Database.Db.First(&hostUser, ride.HostUserID)
			   if result.Error != nil {
					   log.Printf("Error finding host user: %v\n", result.Error)
					   return c.Status(502).JSON(fiber.Map{
							   "error": "Error finding host user",
					   })
			   }

		ridesResponse[i] = RideResponse{
			RideID:        ride.ID,
			HostUserID:    ride.HostUserID,
			HostUserName:  hostUser.Name,
			StartLocation: ride.StartLocation,
			EndLocation:   ride.EndLocation,
			StartTime:     ride.StartTime,
			TotalSeats:    ride.TotalSeats,
			BookedSeats:   ride.BookedSeats,
			TotalPrice:    ride.TotalPrice,
			IsOngoing:     ride.IsOngoing,
			IsSameGender:  ride.IsSameGender,
		}
	}

	return c.Status(200).JSON(ridesResponse)
}

// Function to get a ride by ID
func GetRideByID(c *fiber.Ctx) error {

	rideID := c.Params("id")

	// Start a database transaction
	tx := database.Database.Db.Begin()

	var ride models.Ride

	parsedID, err := uuid.Parse(rideID)
	if err != nil {
		tx.Rollback()
		log.Printf("Invalid rideID format: %v", rideID)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid ride ID format",
		})
	}
	if err := tx.First(&ride, "id = ?", parsedID).Error; err != nil {
		tx.Rollback()
		log.Printf("Ride does not exist")
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	// Find the host user
	var hostUser models.User
	   if err := tx.First(&hostUser, ride.HostUserID).Error; err != nil {
			   // Rollback the transaction in case of an error
			   tx.Rollback()
			   log.Printf("Host user not found")
			   return c.Status(502).JSON(fiber.Map{
					   "error": "Error finding host user",
			   })
	   }

	rideResponse := RideResponse{
		RideID:        ride.ID,
		HostUserID:    ride.HostUserID,
		HostUserName:  hostUser.Name,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		TotalSeats:    ride.TotalSeats,
		BookedSeats:   ride.BookedSeats,
		TotalPrice:    ride.TotalPrice,
		IsOngoing:     ride.IsOngoing,
		IsSameGender:  ride.IsSameGender,
	}

	return c.Status(200).JSON(rideResponse)
}

// Function to update a ride by ID
func UpdateRideByID(c *fiber.Ctx) error {
	rideID := c.Params("id")

	// Host-only auth. The endpoint sits behind the auth middleware
	// so c.Locals("user") is always populated for an authenticated
	// caller; we just need to confirm they're the one who owns the
	// ride. Without this, any authenticated user with a valid ride
	// ID could mutate someone else's ride (price, start time,
	// locations) — a clear privilege gap that was sitting open.
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated"})
	}
	caller, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid user data"})
	}

	var ride models.Ride
	result := database.Database.Db.First(&ride, rideID)

	   if result.Error == gorm.ErrRecordNotFound {
			   log.Printf("Ride with id %v not found\n", rideID)
			   return c.Status(404).JSON(fiber.Map{
					   "error": "Ride not found",
			   })
	   } else if result.Error != nil {
			   log.Printf("Error finding ride: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error finding ride",
			   })
	   }

	if ride.HostUserID != caller.ID {
		log.Printf("User %v attempted to update ride %v owned by %v", caller.ID, ride.ID, ride.HostUserID)
		return c.Status(403).JSON(fiber.Map{
			"error": "Only the host can update this ride",
		})
	}

	var newRide models.Ride
	err := c.BodyParser(&newRide)
	   if err != nil {
			   log.Printf("Error parsing JSON: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error parsing JSON (body parser)",
			   })
	   }

	// Check if the host user ID exists
	var hostUser models.User
	result = database.Database.Db.First(&hostUser, newRide.HostUserID)
	   if result.Error == gorm.ErrRecordNotFound {
			   log.Printf("Host user ID does not exist")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Host user ID does not exist",
			   })
	   } else if result.Error != nil {
			   log.Printf("Error finding host user: %v\n", result.Error)
			   return c.Status(502).JSON(fiber.Map{
					   "error": "Error finding host user",
			   })
	   }

	// Check if start time is in the future
	   if newRide.StartTime.Before(time.Now()) {
			   log.Printf("Start time is in the past")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Start time is in the past",
			   })
	   }

	// Check if the host user has enough seats
	   if newRide.TotalSeats <= newRide.BookedSeats {
			   log.Printf("Total seats available should be more than booked seats")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Total seats available should be more than booked seats",
			   })
	   }

	if newRide.IsSameGender == 1 {
		log.Printf("This ride has been created for the same gender only.")
	} else if newRide.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	   if newRide.TotalPrice < 25 || newRide.TotalPrice > 10000 {
			   log.Printf("Price too low")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Price too low!",
			   })
	   }

	// Snapshot the fields we care about for change-detection BEFORE
	// the Updates() call mutates the row in-place. Anything user-
	// visible (when, where, price) is worth a heads-up; cosmetic
	// changes (chat name, settings) aren't.
	prevStartTime := ride.StartTime
	prevStartLoc := ride.StartLocation
	prevEndLoc := ride.EndLocation
	prevPrice := ride.TotalPrice

	// Update the ride in the database
	result = database.Database.Db.Model(&ride).Updates(newRide)

	   if result.Error != nil {
			   log.Printf("Error updating ride: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error updating ride",
			   })
	   }

	// Notify every accepted passenger if the host changed something
	// material. Builds a short human summary ("time, fare") so the
	// push doesn't just say "ride updated" with no context.
	//
	// 5-MIN COOLDOWN: a host correcting a typo in three rapid edits
	// shouldn't ping every passenger three times. update_notif_last_
	// sent_at on the ride row is the gate — we'll skip the fan-out
	// if a push already went out in the last 5 minutes, and stamp
	// it on every send. The change-detection itself still happens
	// (so a single big edit that lands inside the cooldown window
	// still gets one push if the cooldown has expired by the time
	// the next material change arrives).
	//
	// Async + best-effort — fire-and-forget so the API response
	// isn't held hostage by the FCM round-trip.
	prevNotifSent := ride.UpdateNotifLastSentAt
	go func(rideID uuid.UUID, route string) {
		var changes []string
		if !ride.StartTime.Equal(prevStartTime) {
			changes = append(changes, "time")
		}
		if ride.StartLocation != prevStartLoc {
			changes = append(changes, "pickup")
		}
		if ride.EndLocation != prevEndLoc {
			changes = append(changes, "drop-off")
		}
		if ride.TotalPrice != prevPrice {
			changes = append(changes, "fare")
		}
		if len(changes) == 0 {
			return
		}
		const cooldown = 5 * time.Minute
		if prevNotifSent != nil && time.Since(*prevNotifSent) < cooldown {
			log.Printf("ride-updated notif: cooldown active for ride %s (last sent %s ago); skipping", rideID, time.Since(*prevNotifSent).Round(time.Second))
			return
		}
		summary := strings.Join(changes, ", ")
		fcm := services.GetFCMService()
		if fcm == nil {
			return
		}
		var bookings []models.Booking
		if err := database.Database.Db.
			Where("ride_id = ? AND request_status = ?", rideID, "accepted").
			Find(&bookings).Error; err != nil {
			log.Printf("ride-updated notif: bookings lookup %s: %v", rideID, err)
			return
		}
		// Stamp the cooldown column FIRST so two near-simultaneous
		// updates (in-flight overlap) don't both pass the gate
		// above and double-fire. Marking before send mirrors the
		// trip-today dedup posture: spam-free > resend-on-failure.
		now := time.Now()
		if err := database.Database.Db.Model(&models.Ride{}).
			Where("id = ?", rideID).
			Update("update_notif_last_sent_at", now).Error; err != nil {
			log.Printf("ride-updated notif: stamp cooldown ride=%s: %v", rideID, err)
			// Don't return — sending without the stamp is better
			// than not sending at all. Worst case is a duplicate
			// if another concurrent update happens to land in the
			// next few ms, which is vanishingly rare.
		}
		for _, b := range bookings {
			passengerID := b.PassengerID
			go func() {
				if err := fcm.SendRideUpdatedNotification(passengerID, route, summary, rideID); err != nil {
					log.Printf("ride-updated notif: send to %s: %v", passengerID, err)
				}
			}()
		}
	}(ride.ID, ride.StartLocation+" to "+ride.EndLocation)

	// Track changes, and display them as a result
	rideResponse := RideResponse{
		RideID:        ride.ID,
		HostUserID:    ride.HostUserID,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		TotalSeats:    ride.TotalSeats,
		BookedSeats:   ride.BookedSeats,
		TotalPrice:    ride.TotalPrice,
		IsOngoing:     ride.IsOngoing,
		IsSameGender:  ride.IsSameGender,
	}

	log.Printf("Ride with id %v updated\n", ride.ID)
	return c.Status(200).JSON(rideResponse)
}

// Function to delete a ride by ID
func DeleteRideByID(c *fiber.Ctx) error {
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

	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		log.Printf("Invalid ride ID format: %v\n", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid ride ID format",
		})
	}

	var ride models.Ride
	result := database.Database.Db.Where("id = ?", rideUUID).First(&ride)

	if result.Error == gorm.ErrRecordNotFound {
		log.Printf("Ride with id %v not found\n", rideID)
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	} else if result.Error != nil {
		log.Printf("Error finding ride: %v\n", result.Error)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error finding ride",
		})
	}

	if ride.HostUserID != user.ID {
		log.Printf("User %v is not authorized to delete ride %v (host: %v)\n", user.ID, ride.ID, ride.HostUserID)
		return c.Status(403).JSON(fiber.Map{
			"error": "Only the ride host can delete this ride",
		})
	}

	var acceptedBookingsCount int64
	err = database.Database.Db.Model(&models.Booking{}).
		Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").
		Count(&acceptedBookingsCount).Error
	
	if err != nil {
		log.Printf("Error checking bookings for ride %v: %v\n", rideID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error checking ride bookings",
		})
	}

	if acceptedBookingsCount > 0 {
		log.Printf("Cannot delete ride %v: has %d accepted bookings\n", rideID, acceptedBookingsCount)
		return c.Status(400).JSON(fiber.Map{
			"error":                  "You can't delete this ride — you've already accepted passengers. Reject them first if you really need to cancel.",
			"accepted_booking_count": acceptedBookingsCount,
		})
	}

	// Collect pending bookings BEFORE deletion so we can notify each
	// requester that the post they were waiting on has gone away.
	// Cascade-delete drops these rows along with the ride; without
	// this snapshot we'd lose the user_id list.
	var pendingBookings []models.Booking
	if err := database.Database.Db.
		Where("ride_id = ? AND request_status = ?", rideUUID, "pending").
		Find(&pendingBookings).Error; err != nil {
		log.Printf("Error fetching pending bookings for ride %v: %v\n", rideID, err)
		// Don't block deletion on this — better to drop the post and
		// skip the notification than leave a stale ride around.
	}

	result = database.Database.Db.Delete(&ride)

	if result.Error != nil {
		log.Printf("Error deleting ride: %v\n", result.Error)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error deleting ride",
		})
	}

	// Fire-and-forget pending-passenger notifications. Run in a
	// goroutine so the response doesn't wait on FCM round-trips, and
	// so a transient FCM hiccup doesn't 500 the delete that already
	// committed to the DB.
	if len(pendingBookings) > 0 {
		fcm := services.GetFCMService()
		if fcm != nil {
			pendingCopy := pendingBookings
			rideRoute := ride.StartLocation + " → " + ride.EndLocation
			go func() {
				for _, b := range pendingCopy {
					_ = fcm.SendNotification(
						b.PassengerID,
						"Ride no longer available",
						"The host pulled "+rideRoute+". Find another one — there are usually more on the same route.",
						map[string]string{
							"type":    "ride_cancelled_pending",
							"ride_id": ride.ID.String(),
						},
					)
				}
			}()
		}
	}

	log.Printf("Ride with id %v deleted by user %v (notified %d pending passengers)\n", ride.ID, user.ID, len(pendingBookings))
	return c.Status(200).JSON(fiber.Map{
		"message":             "Ride deleted",
		"pending_notified":    len(pendingBookings),
	})
}
