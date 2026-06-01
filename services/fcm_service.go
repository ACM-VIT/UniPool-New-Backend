package services

import (
	"context"
	"fmt"
	"log"
	"sort"
	"unipool-backend/database"
	"unipool-backend/initializer"

	"firebase.google.com/go/v4/messaging"
	"github.com/google/uuid"
)

type FCMService struct {
	client *messaging.Client
}

var fcmService *FCMService

func dmRoomID(a, b uuid.UUID) string {
	ids := []string{a.String(), b.String()}
	sort.Strings(ids)
	return "dm_" + ids[0] + "_" + ids[1]
}

// InitFCMService initializes the singleton Firebase Cloud Messaging client.
func InitFCMService() error {
	if initializer.FirebaseApp == nil {
		return fmt.Errorf("Firebase app not initialized")
	}

	client, err := initializer.FirebaseApp.Messaging(context.Background())
	if err != nil {
		return fmt.Errorf("error getting Messaging client: %v", err)
	}

	fcmService = &FCMService{client: client}
	log.Println("FCM Service initialized successfully")
	return nil
}

// GetFCMService returns the singleton FCM service instance.
func GetFCMService() *FCMService {
	return fcmService
}

// SendNotification sends a push notification to a specific user.
func (f *FCMService) SendNotification(userID uuid.UUID, title, body string, data map[string]string) error {
	type row struct {
		ID       uuid.UUID `gorm:"column:id"`
		FCMToken string    `gorm:"column:fcm_token"`
	}
	var user row
	if err := database.Database.Db.
		Table("users").
		Select("id, fcm_token").
		Where("id = ?", userID).
		Scan(&user).Error; err != nil {
		return fmt.Errorf("user lookup failed: %v", err)
	}
	if user.ID == uuid.Nil {
		return fmt.Errorf("user not found: %s", userID)
	}

	if user.FCMToken == "" {
		log.Printf("User %s has no FCM token", userID)
		return nil // Not an error, user just doesn't have notifications enabled
	}

	message := &messaging.Message{
		Token: user.FCMToken,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				ChannelID: "default",
				Priority:  messaging.PriorityHigh,
			},
		},
		APNS: &messaging.APNSConfig{
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					Sound: "default",
				},
			},
		},
	}

	response, err := f.client.Send(context.Background(), message)
	if err != nil {
		if messaging.IsInvalidArgument(err) || messaging.IsRegistrationTokenNotRegistered(err) {
			log.Printf("Invalid FCM token for user %s, clearing from database", userID)
			database.Database.Db.Table("users").Where("id = ?", userID).Update("fcm_token", "")
		}
		return fmt.Errorf("error sending message: %v", err)
	}

	log.Printf("Successfully sent message to user %s: %s", userID, response)
	return nil
}

// SendNotificationToMultipleUsers sends notifications to multiple users.
//
// Deprecated: use SendBatch to avoid one database lookup and one FCM request
// per recipient.
func (f *FCMService) SendNotificationToMultipleUsers(userIDs []uuid.UUID, title, body string, data map[string]string) {
	for _, userID := range userIDs {
		go func(id uuid.UUID) {
			if err := f.SendNotification(id, title, body, data); err != nil {
				log.Printf("Error sending notification to user %s: %v", id, err)
			}
		}(userID)
	}
}

// FCMRecipient pairs a user ID with its FCM token. Producers of this
// type batch-load tokens once via LoadFCMTokens; consumers pass the
// slice to SendBatch.
type FCMRecipient struct {
	UserID uuid.UUID
	Token  string
}

// LoadFCMTokens returns one FCMRecipient per user ID with a non-empty token.
// Users without tokens are omitted, matching SendNotification's behavior.
func LoadFCMTokens(userIDs []uuid.UUID) ([]FCMRecipient, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	type row struct {
		ID       uuid.UUID `gorm:"column:id"`
		FCMToken string    `gorm:"column:fcm_token"`
	}
	var rows []row
	if err := database.Database.Db.
		Table("users").
		Select("id, fcm_token").
		Where("id IN ? AND fcm_token IS NOT NULL AND fcm_token <> ''", userIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FCMRecipient, 0, len(rows))
	for _, r := range rows {
		out = append(out, FCMRecipient{UserID: r.ID, Token: r.FCMToken})
	}
	return out, nil
}

// LoadAllowedFCMTokens resolves notification preferences and FCM tokens in one query.
func LoadAllowedFCMTokens(userIDs []uuid.UUID, category string, rideID uuid.UUID) ([]FCMRecipient, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	if category == "" {
		return LoadFCMTokens(userIDs)
	}

	type row struct {
		ID       uuid.UUID `gorm:"column:id"`
		FCMToken string    `gorm:"column:fcm_token"`
	}
	var rows []row
	if err := database.Database.Db.
		Table("users AS u").
		Select("u.id, u.fcm_token").
		Joins(`
			LEFT JOIN notification_preferences gp
			  ON gp.user_id = u.id
			 AND gp.category = ?
			 AND gp.ride_id IS NULL
		`, category).
		Joins(`
			LEFT JOIN notification_preferences rp
			  ON rp.user_id = u.id
			 AND rp.category = ?
			 AND rp.ride_id = ?
		`, category, rideID).
		Where("u.id IN ? AND u.fcm_token IS NOT NULL AND u.fcm_token <> ''", userIDs).
		Where("COALESCE(rp.enabled, gp.enabled, TRUE) = TRUE").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]FCMRecipient, 0, len(rows))
	for _, r := range rows {
		out = append(out, FCMRecipient{UserID: r.ID, Token: r.FCMToken})
	}
	return out, nil
}

// LoadAllowedFCMTokensByRide resolves notification preferences and
// FCM tokens for many ride-scoped recipient sets at once. It keeps
// the same precedence as LoadAllowedFCMTokens:
//
//	per-ride preference > global category preference > default allowed
//
// Scheduler paths use this to avoid one preference/token DB query
// per ride in a tick.
func LoadAllowedFCMTokensByRide(rideUserIDs map[uuid.UUID][]uuid.UUID, category string) (map[uuid.UUID][]FCMRecipient, error) {
	out := make(map[uuid.UUID][]FCMRecipient, len(rideUserIDs))
	if len(rideUserIDs) == 0 {
		return out, nil
	}
	if len(rideUserIDs) == 1 {
		for rideID, userIDs := range rideUserIDs {
			recipients, err := LoadAllowedFCMTokens(userIDs, category, rideID)
			if err != nil {
				return nil, err
			}
			out[rideID] = recipients
		}
		return out, nil
	}

	rideIDSet := make(map[uuid.UUID]struct{}, len(rideUserIDs))
	userIDSet := make(map[uuid.UUID]struct{}, len(rideUserIDs)*2)
	for rideID, userIDs := range rideUserIDs {
		rideIDSet[rideID] = struct{}{}
		for _, userID := range userIDs {
			if userID == uuid.Nil {
				continue
			}
			userIDSet[userID] = struct{}{}
		}
	}
	if len(userIDSet) == 0 {
		return out, nil
	}

	rideIDs := make([]uuid.UUID, 0, len(rideIDSet))
	for rideID := range rideIDSet {
		rideIDs = append(rideIDs, rideID)
	}
	userIDs := make([]uuid.UUID, 0, len(userIDSet))
	for userID := range userIDSet {
		userIDs = append(userIDs, userID)
	}

	type userRow struct {
		ID            uuid.UUID `gorm:"column:id"`
		FCMToken      string    `gorm:"column:fcm_token"`
		GlobalEnabled *bool     `gorm:"column:global_enabled"`
	}
	var users []userRow
	query := database.Database.Db.
		Table("users AS u").
		Select("u.id, u.fcm_token").
		Where("u.id IN ? AND u.fcm_token IS NOT NULL AND u.fcm_token <> ''", userIDs)
	if category != "" {
		query = query.
			Select("u.id, u.fcm_token, gp.enabled AS global_enabled").
			Joins(`
				LEFT JOIN notification_preferences gp
				  ON gp.user_id = u.id
				 AND gp.category = ?
				 AND gp.ride_id IS NULL
			`, category)
	}
	if err := query.Scan(&users).Error; err != nil {
		return nil, err
	}

	tokenByUser := make(map[uuid.UUID]FCMRecipient, len(users))
	globalAllowedByUser := make(map[uuid.UUID]bool, len(users))
	for _, row := range users {
		tokenByUser[row.ID] = FCMRecipient{UserID: row.ID, Token: row.FCMToken}
		globalAllowedByUser[row.ID] = row.GlobalEnabled == nil || *row.GlobalEnabled
	}

	type rideUserKey struct {
		rideID uuid.UUID
		userID uuid.UUID
	}
	rideAllowedByUser := make(map[rideUserKey]bool)
	if category != "" {
		type prefRow struct {
			UserID  uuid.UUID `gorm:"column:user_id"`
			RideID  uuid.UUID `gorm:"column:ride_id"`
			Enabled bool      `gorm:"column:enabled"`
		}
		var prefs []prefRow
		if err := database.Database.Db.
			Table("notification_preferences").
			Select("user_id, ride_id, enabled").
			Where("user_id IN ? AND ride_id IN ? AND category = ?", userIDs, rideIDs, category).
			Scan(&prefs).Error; err != nil {
			return nil, err
		}
		for _, pref := range prefs {
			rideAllowedByUser[rideUserKey{rideID: pref.RideID, userID: pref.UserID}] = pref.Enabled
		}
	}

	for rideID, userIDsForRide := range rideUserIDs {
		recipients := make([]FCMRecipient, 0, len(userIDsForRide))
		seen := make(map[uuid.UUID]struct{}, len(userIDsForRide))
		for _, userID := range userIDsForRide {
			if _, ok := seen[userID]; ok {
				continue
			}
			seen[userID] = struct{}{}

			recipient, hasToken := tokenByUser[userID]
			if !hasToken {
				continue
			}
			allowed, hasRidePreference := rideAllowedByUser[rideUserKey{rideID: rideID, userID: userID}]
			if !hasRidePreference {
				allowed = globalAllowedByUser[userID]
			}
			if allowed {
				recipients = append(recipients, recipient)
			}
		}
		out[rideID] = recipients
	}

	return out, nil
}

// SendBatch fans the same notification out with messaging.SendEach, chunked at
// Firebase's 500-message limit.
//
// Tokens that come back as IsRegistrationTokenNotRegistered (the
// user uninstalled the app, or the token aged out) are cleared
// from the users table in a single batched UPDATE so future sends
// skip them.
//
// Push delivery is best-effort; errors are logged but not propagated.
func (f *FCMService) SendBatch(recipients []FCMRecipient, title, body string, data map[string]string) {
	if f == nil || len(recipients) == 0 {
		return
	}

	const chunkSize = 500 // Firebase SendEach hard cap
	for start := 0; start < len(recipients); start += chunkSize {
		end := start + chunkSize
		if end > len(recipients) {
			end = len(recipients)
		}
		chunk := recipients[start:end]

		messages := make([]*messaging.Message, 0, len(chunk))
		for _, r := range chunk {
			messages = append(messages, &messaging.Message{
				Token:        r.Token,
				Notification: &messaging.Notification{Title: title, Body: body},
				Data:         data,
				Android: &messaging.AndroidConfig{
					Priority: "high",
					Notification: &messaging.AndroidNotification{
						ChannelID: "default",
						Priority:  messaging.PriorityHigh,
					},
				},
				APNS: &messaging.APNSConfig{
					Payload: &messaging.APNSPayload{
						Aps: &messaging.Aps{
							Alert: &messaging.ApsAlert{Title: title, Body: body},
							Sound: "default",
						},
					},
				},
			})
		}

		resp, err := f.client.SendEach(context.Background(), messages)
		if err != nil {
			log.Printf("SendBatch: chunk send failed (size %d): %v", len(messages), err)
			continue
		}

		// Clear invalid tokens in one update after the chunk send completes.
		var deadUserIDs []uuid.UUID
		for i, r := range resp.Responses {
			if r.Success {
				continue
			}
			if messaging.IsInvalidArgument(r.Error) || messaging.IsRegistrationTokenNotRegistered(r.Error) {
				deadUserIDs = append(deadUserIDs, chunk[i].UserID)
			} else if r.Error != nil {
				log.Printf("SendBatch: user %s send failed: %v", chunk[i].UserID, r.Error)
			}
		}
		if len(deadUserIDs) > 0 {
			if err := database.Database.Db.
				Table("users").
				Where("id IN ?", deadUserIDs).
				Update("fcm_token", "").Error; err != nil {
				log.Printf("SendBatch: clearing dead tokens for %d users failed: %v", len(deadUserIDs), err)
			} else {
				log.Printf("SendBatch: cleared FCM tokens for %d users (uninstalled or invalid)", len(deadUserIDs))
			}
		}
	}
}

// Notification helper methods.
func (f *FCMService) SendBookingRequestNotification(rideOwnerID uuid.UUID, passengerID uuid.UUID, passengerName, rideRoute string, rideID uuid.UUID, bookingID uuid.UUID) error {
	title := "New Ride Request"
	body := fmt.Sprintf("%s wants to join your ride to %s", passengerName, rideRoute)
	data := map[string]string{
		"type":           "booking_request",
		"ride_id":        rideID.String(),
		"booking_id":     bookingID.String(),
		"passenger_id":   passengerID.String(),
		"passenger_name": passengerName,
		"dm_room_id":     dmRoomID(rideOwnerID, passengerID),
		"action":         "open_dm",
	}
	return f.SendNotification(rideOwnerID, title, body, data)
}

func (f *FCMService) SendBookingAcceptedNotification(passengerID uuid.UUID, rideRoute string, rideID uuid.UUID, bookingID uuid.UUID) error {
	title := "Ride Request Accepted! 🎉"
	body := fmt.Sprintf("Your request for the ride to %s has been accepted", rideRoute)
	data := map[string]string{
		"type":       "booking_accepted",
		"ride_id":    rideID.String(),
		"booking_id": bookingID.String(),
		"action":     "open_chat",
	}
	return f.SendNotification(passengerID, title, body, data)
}

func (f *FCMService) SendBookingRejectedNotification(passengerID uuid.UUID, rideRoute string, rideID uuid.UUID, bookingID uuid.UUID) error {
	title := "Ride Request Declined"
	body := fmt.Sprintf("Unfortunately, your request for the ride to %s was declined", rideRoute)
	data := map[string]string{
		"type":       "booking_rejected",
		"ride_id":    rideID.String(),
		"booking_id": bookingID.String(),
		"action":     "search_rides",
	}
	return f.SendNotification(passengerID, title, body, data)
}

// SendBookingRemovedByHostNotification tells a confirmed passenger their seat
// was removed by the host.
func (f *FCMService) SendBookingRemovedByHostNotification(passengerID uuid.UUID, rideRoute string, rideID uuid.UUID, bookingID uuid.UUID) error {
	title := "You were removed from a ride"
	body := fmt.Sprintf("The host removed your seat on the ride to %s. Tap to find another.", rideRoute)
	data := map[string]string{
		"type":       "booking_removed_by_host",
		"ride_id":    rideID.String(),
		"booking_id": bookingID.String(),
		"action":     "search_rides",
	}
	return f.SendNotification(passengerID, title, body, data)
}

// SendBookingWithdrawnNotification tells a host that an accepted passenger
// backed out before the ride.
func (f *FCMService) SendBookingWithdrawnNotification(rideOwnerID uuid.UUID, passengerID uuid.UUID, passengerName, rideRoute string, rideID uuid.UUID, bookingID uuid.UUID) error {
	title := "A passenger can't make it"
	body := fmt.Sprintf("%s let you know they can't ride to %s. The seat is open again.", passengerName, rideRoute)
	data := map[string]string{
		"type":           "booking_withdrawn",
		"ride_id":        rideID.String(),
		"booking_id":     bookingID.String(),
		"passenger_id":   passengerID.String(),
		"passenger_name": passengerName,
		"action":         "view_ride",
	}
	return f.SendNotification(rideOwnerID, title, body, data)
}

func (f *FCMService) SendChatMessageNotification(userID uuid.UUID, senderName, message, rideRoute string, rideID uuid.UUID) error {
	title := fmt.Sprintf("New message from %s", senderName)
	body := fmt.Sprintf("💬 %s", message)
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	data := map[string]string{
		"type":    "chat_message",
		"ride_id": rideID.String(),
		"action":  "open_chat",
	}
	return f.SendNotification(userID, title, body, data)
}

func (f *FCMService) SendDirectMessageNotification(userID uuid.UUID, senderID uuid.UUID, senderName, message, dmRoomID string) error {
	title := fmt.Sprintf("New message from %s", senderName)
	body := fmt.Sprintf("💬 %s", message)
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	data := map[string]string{
		"type":        "direct_message",
		"dm_room_id":  dmRoomID,
		"sender_id":   senderID.String(),
		"sender_name": senderName,
		"action":      "open_dm",
	}
	return f.SendNotification(userID, title, body, data)
}

func (f *FCMService) SendRideReminderNotification(userID uuid.UUID, rideRoute string, timeUntilRide string, rideID uuid.UUID) error {
	title := "Ride Reminder"
	body := fmt.Sprintf("Your ride from %s starts in %s", rideRoute, timeUntilRide)
	data := map[string]string{
		"type":    "ride_reminder",
		"ride_id": rideID.String(),
		"action":  "view_ride",
	}
	return f.SendNotification(userID, title, body, data)
}

func (f *FCMService) SendRideCancelledNotification(userID uuid.UUID, rideRoute string, rideID uuid.UUID) error {
	title := "Ride Cancelled"
	body := fmt.Sprintf("The ride to %s has been cancelled by the host", rideRoute)
	data := map[string]string{
		"type":    "ride_cancelled",
		"ride_id": rideID.String(),
		"action":  "search_rides",
	}
	return f.SendNotification(userID, title, body, data)
}

func (f *FCMService) SendRideUpdatedNotification(userID uuid.UUID, rideRoute string, changes string, rideID uuid.UUID) error {
	title := "Ride Updated"
	body := fmt.Sprintf("The ride to %s has been updated: %s", rideRoute, changes)
	data := map[string]string{
		"type":    "ride_updated",
		"ride_id": rideID.String(),
		"action":  "view_ride",
	}
	return f.SendNotification(userID, title, body, data)
}
