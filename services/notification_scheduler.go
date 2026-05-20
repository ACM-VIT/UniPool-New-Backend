package services

import (
	"fmt"
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
		ns.checkRideReminders()
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

// checkRideReminders fires a single "your ride is in ~20 minutes" push
// to host + accepted passengers shortly before each trip's scheduled
// start.
//
// Window strategy: the previous version matched any ride starting in
// the next 30 minutes, but the scheduler ticks every 10 minutes — so
// the same ride landed in 3 consecutive queries and the user got 3
// reminder pushes. Now we use a *sliding* 10-minute window matched to
// the tick (the same idiom checkRatingPrompts uses):
//
//   start_time ∈ [now + 15m, now + 25m)
//
// With ticks every 10 minutes this window is exactly tick-width, so
// every ride passes through it in exactly ONE tick. Half-open so a
// ride whose start_time lands exactly on the boundary isn't double-
// counted by adjacent ticks.
func (ns *NotificationScheduler) checkRideReminders() {
	now := time.Now()

	windowStart := now.Add(15 * time.Minute)
	windowEnd := now.Add(25 * time.Minute)

	var upcomingRides []models.Ride
	if err := database.Database.Db.Preload("HostUser").
		Where("start_time >= ? AND start_time < ?", windowStart, windowEnd).
		Find(&upcomingRides).Error; err != nil {
		log.Printf("Error fetching upcoming rides: %v", err)
		return
	}

	for _, ride := range upcomingRides {
		timeUntilRide := ride.StartTime.Sub(now)
		timeString := formatDuration(timeUntilRide)
		rideRoute := ride.StartLocation + " to " + ride.EndLocation

		// Send reminder to host (respect their preferences).
		go func(r models.Ride) {
			if allowed, _ := helpers.IsNotificationAllowed(r.HostUserID, helpers.NotifTripReminders, r.ID); !allowed {
				return
			}
			if err := ns.fcmService.SendRideReminderNotification(
				r.HostUserID,
				rideRoute,
				timeString,
				r.ID,
			); err != nil {
				log.Printf("Error sending ride reminder to host %s: %v", r.HostUserID, err)
			}
		}(ride)

		// Get all accepted bookings for this ride
		var bookings []models.Booking
		if err := database.Database.Db.Where("ride_id = ? AND request_status = ?", ride.ID, "accepted").
			Find(&bookings).Error; err != nil {
			log.Printf("Error fetching bookings for ride %s: %v", ride.ID, err)
			continue
		}

		// Send reminders to all passengers (each respects their own
		// trip-reminder preference).
		for _, booking := range bookings {
			go func(b models.Booking, route, timeStr string, rideID string) {
				if allowed, _ := helpers.IsNotificationAllowed(b.PassengerID, helpers.NotifTripReminders, ride.ID); !allowed {
					return
				}
				if err := ns.fcmService.SendRideReminderNotification(
					b.PassengerID,
					route,
					timeStr,
					ride.ID,
				); err != nil {
					log.Printf("Error sending ride reminder to passenger %s: %v", b.PassengerID, err)
				}
			}(booking, rideRoute, timeString, ride.ID.String())
		}
	}
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

// formatDuration formats a time duration into a human-readable string
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	
	minutes := int(d.Minutes())
	if minutes < 60 {
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	
	hours := minutes / 60
	remainingMinutes := minutes % 60
	
	if hours == 1 {
		if remainingMinutes == 0 {
			return "1 hour"
		}
		if remainingMinutes == 1 {
			return "1 hour 1 minute"
		}
		return fmt.Sprintf("1 hour %d minutes", remainingMinutes)
	}
	
	if remainingMinutes == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	if remainingMinutes == 1 {
		return fmt.Sprintf("%d hours 1 minute", hours)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, remainingMinutes)
}
