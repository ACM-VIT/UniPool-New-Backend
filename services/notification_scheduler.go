package services

import (
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/google/uuid"
)

type NotificationScheduler struct {
	fcmService *FCMService
}

var notificationScheduler *NotificationScheduler

// InitNotificationScheduler initializes the notification scheduler
func InitNotificationScheduler() {
	fcmService := GetFCMService()
	if fcmService == nil {
		log.Println("FCM service not available, notification scheduler disabled")
		return
	}

	notificationScheduler = &NotificationScheduler{
		fcmService: fcmService,
	}

	// Start the scheduler
	go notificationScheduler.startScheduler()
	log.Println("Notification scheduler initialized and started")
}

// GetNotificationScheduler returns the notification scheduler instance
func GetNotificationScheduler() *NotificationScheduler {
	return notificationScheduler
}

// startScheduler runs the background scheduler
func (ns *NotificationScheduler) startScheduler() {
	ticker := time.NewTicker(10 * time.Minute) // Check every 10 minutes
	defer ticker.Stop()

	for range ticker.C {
		// checkRideReminders was retired — it pushed 1 reminder per
		// ride in a 15-25 minute window, which felt like spam in
		// practice. Day-before email (with .ics) is now the only
		// scheduled pre-trip touchpoint; tap-to-pay + group chat
		// handle everything in the trip-day window.
		ns.checkTripTodayEmails()
		ns.checkRatingPrompts()
	}
}

// checkRatingPrompts fires a "how was the ride?" push to host +
// accepted passengers ~12 hours after a trip's scheduled start.
//
// The window is `[start_time + 12h - tick, start_time + 12h]` —
// matched to our 10-minute tick so each ride lands in exactly one
// window without needing a per-ride "notified" flag. If the
// scheduler is briefly down we miss the push, but the in-app
// prompt (driven by /ride/:id/rating-eligibility on app open)
// still catches them.
func (ns *NotificationScheduler) checkRatingPrompts() {
	now := time.Now()
	windowEnd := now.Add(-12 * time.Hour)
	windowStart := windowEnd.Add(-10 * time.Minute)

	var rides []models.Ride
	if err := database.Database.Db.
		Where("start_time BETWEEN ? AND ?", windowStart, windowEnd).
		Find(&rides).Error; err != nil {
		log.Printf("checkRatingPrompts: query: %v", err)
		return
	}

	for _, ride := range rides {
		rideRoute := ride.StartLocation + " → " + ride.EndLocation

		// Host first (respect rating-prompt preference).
		go func(r models.Ride, route string) {
			if allowed, _ := helpers.IsNotificationAllowed(r.HostUserID, helpers.NotifRatingPrompts, r.ID); !allowed {
				return
			}
			_ = ns.fcmService.SendNotification(
				r.HostUserID,
				"How was the ride?",
				"Tap to rate your passengers on "+route+". Takes 5 seconds.",
				map[string]string{
					"type":    "rating_prompt",
					"ride_id": r.ID.String(),
				},
			)
		}(ride, rideRoute)

		// Then every accepted passenger.
		var bookings []models.Booking
		if err := database.Database.Db.
			Where("ride_id = ? AND request_status = ?", ride.ID, "accepted").
			Find(&bookings).Error; err != nil {
			log.Printf("checkRatingPrompts: bookings %s: %v", ride.ID, err)
			continue
		}
		for _, b := range bookings {
			go func(passengerID uuid.UUID, rideID string, route string, rUUID uuid.UUID) {
				if allowed, _ := helpers.IsNotificationAllowed(passengerID, helpers.NotifRatingPrompts, rUUID); !allowed {
					return
				}
				_ = ns.fcmService.SendNotification(
					passengerID,
					"How was the ride?",
					"Tap to rate your host on "+route+". Takes 5 seconds.",
					map[string]string{
						"type":    "rating_prompt",
						"ride_id": rideID,
					},
				)
			}(b.PassengerID, ride.ID.String(), rideRoute, ride.ID)
		}
	}
}

// checkTripTodayEmails fires the day-before "your trip is tomorrow"
// email to host + accepted passengers. Replaces the noisy 15-25min
// push reminder — users get one well-designed email per trip with an
// .ics calendar invite attached, instead of being pinged 20 min before
// they were already going to leave.
//
// Window strategy: rides with start_time in [now + 12h, now + 36h)
// that haven't been emailed yet. The 24-hour-wide window means the
// scheduler can catch every ride at *some* point in the day-before,
// even if it was created less than 24h before the trip. Per-ride
// dedup via the `trip_today_email_sent_at` column on rides — set
// atomically before the email actually sends, so a scheduler restart
// or overlapping tick can't double-fire. (We accept the rare race
// where a tick crashes between marking sent and actually sending —
// missing one email is better than spamming the user twice.)
func (ns *NotificationScheduler) checkTripTodayEmails() {
	now := time.Now()
	windowStart := now.Add(12 * time.Hour)
	windowEnd := now.Add(36 * time.Hour)

	var rides []models.Ride
	if err := database.Database.Db.Preload("HostUser").
		Where("start_time >= ? AND start_time < ? AND trip_today_email_sent_at IS NULL", windowStart, windowEnd).
		Find(&rides).Error; err != nil {
		log.Printf("checkTripTodayEmails: ride query: %v", err)
		return
	}

	for _, ride := range rides {
		// Mark sent atomically BEFORE the actual send. The partial
		// index on (start_time WHERE trip_today_email_sent_at IS
		// NULL) means this single UPDATE acts as a lease — any
		// concurrent tick that hits the same row sees 0 rows
		// updated and skips. Trade-off: if SES fails after this,
		// the user gets no email. Acceptable; spamming is worse.
		stamp := time.Now()
		res := database.Database.Db.Model(&models.Ride{}).
			Where("id = ? AND trip_today_email_sent_at IS NULL", ride.ID).
			Update("trip_today_email_sent_at", stamp)
		if res.Error != nil {
			log.Printf("checkTripTodayEmails: mark sent %s: %v", ride.ID, res.Error)
			continue
		}
		if res.RowsAffected == 0 {
			// Lost the race to another tick; skip.
			continue
		}

		// Host first.
		go func(r models.Ride) {
			if r.HostUser.Email == "" {
				return
			}
			if err := helpers.SendTripTodayEmail(helpers.TripTodayEmailParams{
				ToEmail:       r.HostUser.Email,
				ToName:        r.HostUser.Name,
				RideID:        r.ID.String(),
				StartLocation: r.StartLocation,
				EndLocation:   r.EndLocation,
				StartTime:     r.StartTime,
				HostName:      r.HostUser.Name,
				HostFirst:     firstName(r.HostUser.Name),
				TotalPrice:    r.TotalPrice,
				IsHost:        true,
			}); err != nil {
				log.Printf("checkTripTodayEmails: host email %s: %v", r.HostUser.Email, err)
			}
		}(ride)

		// Then every accepted passenger.
		var bookings []models.Booking
		if err := database.Database.Db.Preload("Passenger").
			Where("ride_id = ? AND request_status = ?", ride.ID, "accepted").
			Find(&bookings).Error; err != nil {
			log.Printf("checkTripTodayEmails: bookings %s: %v", ride.ID, err)
			continue
		}
		for _, b := range bookings {
			go func(b models.Booking, r models.Ride) {
				if b.Passenger.Email == "" {
					return
				}
				if err := helpers.SendTripTodayEmail(helpers.TripTodayEmailParams{
					ToEmail:       b.Passenger.Email,
					ToName:        b.Passenger.Name,
					RideID:        r.ID.String(),
					StartLocation: r.StartLocation,
					EndLocation:   r.EndLocation,
					StartTime:     r.StartTime,
					HostName:      r.HostUser.Name,
					HostFirst:     firstName(r.HostUser.Name),
					TotalPrice:    r.TotalPrice,
					IsHost:        false,
				}); err != nil {
					log.Printf("checkTripTodayEmails: passenger email %s: %v", b.Passenger.Email, err)
				}
			}(b, ride)
		}
	}
}

// firstName returns the first whitespace-separated token of a name,
// or the original string if it's a single word. Used to render
// "Hop in with {firstName}" warmly without using the full name.
func firstName(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == ' ' {
			return full[:i]
		}
	}
	return full
}

// ScheduleRideCancellationNotifications sends notifications when a ride is cancelled
func (ns *NotificationScheduler) ScheduleRideCancellationNotifications(rideID string) {
	var ride models.Ride
	if err := database.Database.Db.Preload("HostUser").First(&ride, "id = ?", rideID).Error; err != nil {
		log.Printf("Error fetching ride for cancellation notifications: %v", err)
		return
	}

	rideRoute := ride.StartLocation + " to " + ride.EndLocation

	// Get all accepted bookings for this ride
	var bookings []models.Booking
	if err := database.Database.Db.Where("ride_id = ? AND request_status = ?", rideID, "accepted").
		Find(&bookings).Error; err != nil {
		log.Printf("Error fetching bookings for cancelled ride: %v", err)
		return
	}

	// Send cancellation notifications to all passengers
	for _, booking := range bookings {
		go func(passengerID string) {
			if err := ns.fcmService.SendRideCancelledNotification(
				booking.PassengerID, 
				rideRoute, 
				ride.ID,
			); err != nil {
				log.Printf("Error sending cancellation notification to passenger %s: %v", passengerID, err)
			}
		}(booking.PassengerID.String())
	}
}

// (formatDuration humaniser removed alongside checkRideReminders.
// Day-before email uses time.Format directly; no humaniser needed.)
