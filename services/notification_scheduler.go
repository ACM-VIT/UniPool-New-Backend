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

// InitNotificationScheduler starts background notification jobs when FCM is available.
func InitNotificationScheduler() {
	fcmService := GetFCMService()
	if fcmService == nil {
		log.Println("FCM service not available, notification scheduler disabled")
		return
	}

	notificationScheduler = &NotificationScheduler{
		fcmService: fcmService,
	}

	go notificationScheduler.startScheduler()
	log.Println("Notification scheduler initialized and started")
}

// GetNotificationScheduler returns the process-wide scheduler instance.
func GetNotificationScheduler() *NotificationScheduler {
	return notificationScheduler
}

// startScheduler runs all recurring notification jobs on a shared tick.
func (ns *NotificationScheduler) startScheduler() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		ns.checkTripTodayEmails()
		ns.checkRatingPrompts()
	}
}

// checkRatingPrompts sends post-trip rating prompts to hosts and accepted
// passengers about 12 hours after the scheduled start.
//
// The 10-minute lookback matches the scheduler tick, so each ride is eligible
// once without a per-ride notification flag. If this job misses a tick, the
// in-app rating eligibility check still surfaces the prompt.
func (ns *NotificationScheduler) checkRatingPrompts() {
	now := time.Now()
	windowEnd := now.Add(-12 * time.Hour)
	windowStart := windowEnd.Add(-10 * time.Minute)

	type ratingRideRow struct {
		ID            uuid.UUID `gorm:"column:id"`
		HostUserID    uuid.UUID `gorm:"column:host_user_id"`
		StartLocation string    `gorm:"column:start_location"`
		EndLocation   string    `gorm:"column:end_location"`
	}
	var rides []ratingRideRow
	if err := database.Database.Db.
		Model(&models.Ride{}).
		Select("id, host_user_id, start_location, end_location").
		Where("start_time BETWEEN ? AND ?", windowStart, windowEnd).
		Find(&rides).Error; err != nil {
		log.Printf("checkRatingPrompts: query: %v", err)
		return
	}
	if len(rides) == 0 {
		return
	}

	rideIDs := make([]uuid.UUID, 0, len(rides))
	for _, ride := range rides {
		rideIDs = append(rideIDs, ride.ID)
	}

	type ratingBookingRow struct {
		RideID      uuid.UUID `gorm:"column:ride_id"`
		PassengerID uuid.UUID `gorm:"column:passenger_id"`
	}
	var bookings []ratingBookingRow
	if err := database.Database.Db.
		Model(&models.Booking{}).
		Select("ride_id, passenger_id").
		Where("ride_id IN ? AND request_status = ?", rideIDs, "accepted").
		Find(&bookings).Error; err != nil {
		log.Printf("checkRatingPrompts: bookings query: %v", err)
		return
	}

	bookingsByRide := make(map[uuid.UUID][]uuid.UUID, len(rides))
	for _, booking := range bookings {
		bookingsByRide[booking.RideID] = append(bookingsByRide[booking.RideID], booking.PassengerID)
	}

	recipientIDsByRide := make(map[uuid.UUID][]uuid.UUID, len(rides))
	for _, ride := range rides {
		passengerIDs := bookingsByRide[ride.ID]
		userIDs := make([]uuid.UUID, 0, 1+len(passengerIDs))
		userIDs = append(userIDs, ride.HostUserID)
		userIDs = append(userIDs, passengerIDs...)
		recipientIDsByRide[ride.ID] = userIDs
	}
	recipientsByRide, err := LoadAllowedFCMTokensByRide(recipientIDsByRide, helpers.NotifRatingPrompts)
	if err != nil {
		log.Printf("checkRatingPrompts: batched allowed-token lookup failed: %v", err)
		recipientsByRide = nil
	}

	for _, ride := range rides {
		rideRoute := ride.StartLocation + " → " + ride.EndLocation
		passengerIDs := bookingsByRide[ride.ID]
		userIDs := recipientIDsByRide[ride.ID]
		recipients := recipientsByRide[ride.ID]
		if recipientsByRide == nil {
			recipients = loadAllowedSchedulerRecipients(
				userIDs,
				helpers.NotifRatingPrompts,
				ride.ID,
			)
		}

		go func(r ratingRideRow, route string, passengers []uuid.UUID, recipients []FCMRecipient) {
			if len(recipients) == 0 {
				return
			}

			passengerSet := make(map[uuid.UUID]struct{}, len(passengers))
			for _, id := range passengers {
				passengerSet[id] = struct{}{}
			}
			hostRecipients := make([]FCMRecipient, 0, 1)
			passengerRecipients := make([]FCMRecipient, 0, len(recipients))
			for _, recipient := range recipients {
				if recipient.UserID == r.HostUserID {
					hostRecipients = append(hostRecipients, recipient)
					continue
				}
				if _, ok := passengerSet[recipient.UserID]; ok {
					passengerRecipients = append(passengerRecipients, recipient)
				}
			}

			ns.fcmService.SendBatch(
				hostRecipients,
				"How was the ride?",
				"Tap to rate your passengers on "+route+". Takes 5 seconds.",
				map[string]string{
					"type":    "rating_prompt",
					"ride_id": r.ID.String(),
				},
			)
			ns.fcmService.SendBatch(
				passengerRecipients,
				"How was the ride?",
				"Tap to rate your host on "+route+". Takes 5 seconds.",
				map[string]string{
					"type":    "rating_prompt",
					"ride_id": r.ID.String(),
				},
			)
		}(ride, rideRoute, passengerIDs, recipients)
	}
}

// checkTripTodayEmails sends the day-before trip email to hosts and accepted
// passengers, including the calendar invite generated by helpers.SendTripTodayEmail.
//
// Window: rides with start_time in [now + 1h, now + 36h). The lower bound
// avoids sending for trips that are already about to begin; the upper bound
// ensures tomorrow's cohort is scanned by at least one scheduler tick.
//
// Deduplication is per (ride, user) in trip_today_emails_sent. The insert is
// also the lease: a primary-key conflict means a prior tick or goroutine has
// already handled that recipient. See migration 00006_email_dedup_rework.sql.
func (ns *NotificationScheduler) checkTripTodayEmails() {
	now := time.Now()
	windowStart := now.Add(1 * time.Hour)
	windowEnd := now.Add(36 * time.Hour)

	type tripTodayCandidate struct {
		RideID         uuid.UUID `gorm:"column:ride_id"`
		StartLocation  string    `gorm:"column:start_location"`
		EndLocation    string    `gorm:"column:end_location"`
		StartTime      time.Time `gorm:"column:start_time"`
		TotalPrice     uint      `gorm:"column:total_price"`
		HostName       string    `gorm:"column:host_name"`
		RecipientID    uuid.UUID `gorm:"column:recipient_id"`
		RecipientEmail string    `gorm:"column:recipient_email"`
		RecipientName  string    `gorm:"column:recipient_name"`
		IsHost         bool      `gorm:"column:is_host"`
	}
	var candidates []tripTodayCandidate
	if err := database.Database.Db.Raw(`
		WITH ride_window AS (
			SELECT r.id AS ride_id,
			       r.start_location,
			       r.end_location,
			       r.start_time,
			       r.total_price,
			       r.host_user_id,
			       h.name AS host_name,
			       h.email AS host_email
			  FROM rides r
			  JOIN users h ON h.id = r.host_user_id AND h.deleted_at IS NULL
			 WHERE r.deleted_at IS NULL
			   AND r.start_time >= ?
			   AND r.start_time < ?
		)
		SELECT rw.ride_id,
		       rw.start_location,
		       rw.end_location,
		       rw.start_time,
		       rw.total_price,
		       rw.host_name,
		       rw.host_user_id AS recipient_id,
		       rw.host_email AS recipient_email,
		       rw.host_name AS recipient_name,
		       TRUE AS is_host
		  FROM ride_window rw
		UNION ALL
		SELECT rw.ride_id,
		       rw.start_location,
		       rw.end_location,
		       rw.start_time,
		       rw.total_price,
		       rw.host_name,
		       p.id AS recipient_id,
		       p.email AS recipient_email,
		       p.name AS recipient_name,
		       FALSE AS is_host
		  FROM ride_window rw
		  JOIN bookings b
		    ON b.ride_id = rw.ride_id
		   AND b.deleted_at IS NULL
		   AND b.request_status = 'accepted'
		  JOIN users p ON p.id = b.passenger_id AND p.deleted_at IS NULL
	`, windowStart, windowEnd).Scan(&candidates).Error; err != nil {
		log.Printf("checkTripTodayEmails: candidate query: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	for _, candidate := range candidates {
		go ns.tryTripTodayEmailCandidate(candidate)
	}
}

func loadAllowedSchedulerRecipients(userIDs []uuid.UUID, category string, rideID uuid.UUID) []FCMRecipient {
	recipients, err := LoadAllowedFCMTokens(userIDs, category, rideID)
	if err == nil {
		return recipients
	}
	log.Printf("notification scheduler: allowed-token lookup failed category=%s ride=%s: %v", category, rideID, err)

	recipients, err = LoadFCMTokens(userIDs)
	if err != nil {
		log.Printf("notification scheduler: token fallback failed category=%s ride=%s: %v", category, rideID, err)
		return nil
	}
	return recipients
}

// tryTripTodayEmail sends a single trip email after leasing the (ride, user)
// slot with INSERT ... ON CONFLICT DO NOTHING.
//
// The lease is taken before SES send. A send failure after the insert will not
// be retried automatically; this avoids duplicate trip emails across workers.
func (ns *NotificationScheduler) tryTripTodayEmail(ride models.Ride, recipient models.User, isHost bool) {
	if recipient.Email == "" || recipient.ID == uuid.Nil {
		return
	}
	row := models.TripTodayEmailSent{
		RideID: ride.ID,
		UserID: recipient.ID,
	}
	res := database.Database.Db.
		// RowsAffected == 1 means this worker owns the send.
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

func (ns *NotificationScheduler) tryTripTodayEmailCandidate(candidate struct {
	RideID         uuid.UUID `gorm:"column:ride_id"`
	StartLocation  string    `gorm:"column:start_location"`
	EndLocation    string    `gorm:"column:end_location"`
	StartTime      time.Time `gorm:"column:start_time"`
	TotalPrice     uint      `gorm:"column:total_price"`
	HostName       string    `gorm:"column:host_name"`
	RecipientID    uuid.UUID `gorm:"column:recipient_id"`
	RecipientEmail string    `gorm:"column:recipient_email"`
	RecipientName  string    `gorm:"column:recipient_name"`
	IsHost         bool      `gorm:"column:is_host"`
}) {
	if candidate.RecipientEmail == "" || candidate.RecipientID == uuid.Nil {
		return
	}
	res := database.Database.Db.Exec(
		`INSERT INTO trip_today_emails_sent (ride_id, user_id, sent_at)
		 VALUES (?, ?, NOW())
		 ON CONFLICT (ride_id, user_id) DO NOTHING`,
		candidate.RideID, candidate.RecipientID,
	)
	if res.Error != nil {
		log.Printf("tryTripTodayEmailCandidate: dedup insert ride=%s user=%s: %v", candidate.RideID, candidate.RecipientID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return
	}

	if err := helpers.SendTripTodayEmail(helpers.TripTodayEmailParams{
		ToEmail:       candidate.RecipientEmail,
		ToName:        candidate.RecipientName,
		RideID:        candidate.RideID.String(),
		StartLocation: candidate.StartLocation,
		EndLocation:   candidate.EndLocation,
		StartTime:     candidate.StartTime,
		HostName:      candidate.HostName,
		HostFirst:     firstName(candidate.HostName),
		TotalPrice:    candidate.TotalPrice,
		IsHost:        candidate.IsHost,
	}); err != nil {
		log.Printf("tryTripTodayEmailCandidate: SES send ride=%s user=%s: %v", candidate.RideID, candidate.RecipientID, err)
	}
}

// firstName returns the first whitespace-separated token of a display name.
func firstName(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == ' ' {
			return full[:i]
		}
	}
	return full
}

// ScheduleRideCancellationNotifications notifies accepted passengers when a ride is cancelled.
func (ns *NotificationScheduler) ScheduleRideCancellationNotifications(rideID string) {
	var ride models.Ride
	if err := database.Database.Db.
		Select("id", "start_location", "end_location").
		First(&ride, "id = ?", rideID).Error; err != nil {
		log.Printf("Error fetching ride for cancellation notifications: %v", err)
		return
	}

	rideRoute := ride.StartLocation + " to " + ride.EndLocation

	var bookings []struct {
		PassengerID uuid.UUID `gorm:"column:passenger_id"`
	}
	if err := database.Database.Db.Model(&models.Booking{}).
		Select("passenger_id").
		Where("ride_id = ? AND request_status = ?", rideID, "accepted").
		Find(&bookings).Error; err != nil {
		log.Printf("Error fetching bookings for cancelled ride: %v", err)
		return
	}

	passengerIDs := make([]uuid.UUID, 0, len(bookings))
	for _, booking := range bookings {
		passengerIDs = append(passengerIDs, booking.PassengerID)
	}
	recipients, err := LoadFCMTokens(passengerIDs)
	if err != nil {
		log.Printf("Error fetching cancellation notification tokens for ride %s: %v", ride.ID, err)
		return
	}
	go ns.fcmService.SendBatch(
		recipients,
		"Ride Cancelled",
		"The ride to "+rideRoute+" has been cancelled by the host",
		map[string]string{
			"type":    "ride_cancelled",
			"ride_id": ride.ID.String(),
			"action":  "search_rides",
		},
	)
}
