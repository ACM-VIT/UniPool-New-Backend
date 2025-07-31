package services

import (
	"fmt"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"
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
	}
}

// checkRideReminders checks for upcoming rides and sends reminders
func (ns *NotificationScheduler) checkRideReminders() {
	now := time.Now()
	
	// Find rides starting in the next 30 minutes
	thirtyMinutesFromNow := now.Add(30 * time.Minute)
	
	var upcomingRides []models.Ride
	if err := database.Database.Db.Preload("HostUser").
		Where("start_time BETWEEN ? AND ? AND start_time > ?", now, thirtyMinutesFromNow, now).
		Find(&upcomingRides).Error; err != nil {
		log.Printf("Error fetching upcoming rides: %v", err)
		return
	}

	for _, ride := range upcomingRides {
		timeUntilRide := ride.StartTime.Sub(now)
		timeString := formatDuration(timeUntilRide)
		rideRoute := ride.StartLocation + " to " + ride.EndLocation

		// Send reminder to host
		go func(r models.Ride) {
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

		// Send reminders to all passengers
		for _, booking := range bookings {
			go func(b models.Booking, route, timeStr string, rideID string) {
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
