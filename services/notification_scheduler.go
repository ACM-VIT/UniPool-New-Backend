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
// Window: rides with start_time in [now + 1h, now + 36h). The 1-hour
// floor (down from 12h in 00005) catches last-minute rides created
// shortly before they start — the previous floor silently missed any
// ride created < 12h before takeoff. Top of window is still 36h so
// the day-before cohort gets at least one tick to land in.
//
// Dedup is per-(ride, user) via the trip_today_emails_sent table.
// On every tick we walk every recipient candidate and try to INSERT
// the dedup row first; the insert IS the lease (PK conflict means
// someone — earlier tick, parallel goroutine — already sent). This
// supersedes the old per-ride dedup column, which couldn't handle
// late-accepted passengers (host accepts at 10pm after the 8pm
// scheduler tick already marked the ride sent → late passenger
// never got the email). See migration 00006_email_dedup_rework.sql.
func (ns *NotificationScheduler) checkTripTodayEmails() {
	now := time.Now()
	windowStart := now.Add(1 * time.Hour)
	windowEnd := now.Add(36 * time.Hour)

	var rides []models.Ride
	if err := database.Database.Db.Preload("HostUser").
		Where("start_time >= ? AND start_time < ?", windowStart, windowEnd).
		Find(&rides).Error; err != nil {
		log.Printf("checkTripTodayEmails: ride query: %v", err)
		return
	}

	for _, ride := range rides {
		// Host first.
		go ns.tryTripTodayEmail(ride, ride.HostUser, true)

		// Then every accepted passenger.
		var bookings []models.Booking
		if err := database.Database.Db.Preload("Passenger").
			Where("ride_id = ? AND request_status = ?", ride.ID, "accepted").
			Find(&bookings).Error; err != nil {
			log.Printf("checkTripTodayEmails: bookings %s: %v", ride.ID, err)
			continue
		}
		for _, b := range bookings {
			go ns.tryTripTodayEmail(ride, b.Passenger, false)
		}
	}
}

// tryTripTodayEmail leases the (ride, user) slot via an
// INSERT ... ON CONFLICT DO NOTHING and only sends if we won
// the lease. Splitting this out keeps the scheduler loop readable
// and makes future "manual resend" code paths free — anyone can
// call this with a (ride, user) and the dedup handles itself.
//
// Trade-off (documented in 00005, preserved here): the lease is
// taken BEFORE the SES send. If SES fails after the lease lands,
// that recipient never gets the email even on retry. Missing one
// email > spamming twice; flag if you want a different posture.
func (ns *NotificationScheduler) tryTripTodayEmail(ride models.Ride, recipient models.User, isHost bool) {
	if recipient.Email == "" || recipient.ID == uuid.Nil {
		return
	}
	row := models.TripTodayEmailSent{
		RideID: ride.ID,
		UserID: recipient.ID,
	}
	res := database.Database.Db.
		// CockroachDB / Postgres ON CONFLICT DO NOTHING — silently
		// skips if (ride_id, user_id) already exists. RowsAffected
		// == 1 means we got the lease; 0 means we lost the race.
		Exec(
			`INSERT INTO trip_today_emails_sent (ride_id, user_id, sent_at)
			 VALUES (?, ?, NOW())
			 ON CONFLICT (ride_id, user_id) DO NOTHING`,
			row.RideID, row.UserID,
		)
	if res.Error != nil {
		log.Printf("tryTripTodayEmail: dedup insert ride=%s user=%s: %v", ride.ID, recipient.ID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // already sent
	}

	if err := helpers.SendTripTodayEmail(helpers.TripTodayEmailParams{
		ToEmail:       recipient.Email,
		ToName:        recipient.Name,
		RideID:        ride.ID.String(),
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		HostName:      ride.HostUser.Name,
		HostFirst:     firstName(ride.HostUser.Name),
		TotalPrice:    ride.TotalPrice,
		IsHost:        isHost,
	}); err != nil {
		log.Printf("tryTripTodayEmail: SES send ride=%s user=%s: %v", ride.ID, recipient.ID, err)
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
