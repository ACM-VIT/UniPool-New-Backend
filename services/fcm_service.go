package services

import (
	"context"
	"fmt"
	"log"
	"sort"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/models"

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

// InitFCMService initializes the FCM service
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

// GetFCMService returns the singleton FCM service instance
func GetFCMService() *FCMService {
	return fcmService
}

// SendNotification sends a push notification to a specific user
func (f *FCMService) SendNotification(userID uuid.UUID, title, body string, data map[string]string) error {
	// Get user's FCM token from database
	var user models.User
	if err := database.Database.Db.First(&user, userID).Error; err != nil {
		return fmt.Errorf("user not found: %v", err)
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
		// Check if token is invalid and clear it from database
		if messaging.IsInvalidArgument(err) || messaging.IsRegistrationTokenNotRegistered(err) {
			log.Printf("Invalid FCM token for user %s, clearing from database", userID)
			database.Database.Db.Model(&user).Update("fcm_token", "")
		}
		return fmt.Errorf("error sending message: %v", err)
	}

	log.Printf("Successfully sent message to user %s: %s", userID, response)
	return nil
}

// SendNotificationToMultipleUsers sends notifications to multiple users
//
// Deprecated: this fans out N goroutines each doing its own
// First(&user) lookup and its own messaging.Send() HTTP call. Use
// SendBatch instead — one DB round-trip + one batched FCM call,
// regardless of recipient count.
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

// LoadFCMTokens returns one FCMRecipient per user ID that has a
// non-empty fcm_token, in a single DB round-trip. Users with no token
// (notifications never enabled, or token cleared after an invalid
// send) are omitted silently — matching SendNotification's existing
// "no token is not an error" posture.
//
// Pre-fix every push call site paid a First(&user) per recipient
// just to read the FCMToken column. For a 10-person chat fan-out
// that's ten 25ms round-trips before any FCM HTTP fires.
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

// SendBatch fans the same notification out to every recipient in ONE
// FCM HTTP call via messaging.SendEach. Firebase caps each call at
// 500 messages — we chunk above that to stay safe, although every
// current call site (chat fan-out, accept/reject) is well under.
//
// Tokens that come back as IsRegistrationTokenNotRegistered (the
// user uninstalled the app, or the token aged out) are cleared
// from the users table in a single batched UPDATE so future sends
// skip them.
//
// Returns nothing — push is best-effort. Errors and per-token
// failures are logged but never propagated; callers fire-and-forget
// just like SendNotification's existing per-token call sites.
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

		// Collect the user IDs whose tokens are dead so we can wipe
		// them in a single UPDATE rather than per-row.
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

// Notification types and helper methods
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

// SendBookingWithdrawnNotification is sent to the HOST when an
// accepted passenger backs out before the ride happens. Same data
// shape as the other booking-event pushes so the client's notification
// router can deep-link to the ride details page cleanly.
//
// Wording avoids blame ("can't make it" rather than "cancelled"):
// the goal is a graceful release, not a shame signal.
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
