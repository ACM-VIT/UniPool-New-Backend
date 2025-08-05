package services

import (
	"context"
	"fmt"
	"log"
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
func (f *FCMService) SendNotificationToMultipleUsers(userIDs []uuid.UUID, title, body string, data map[string]string) {
	for _, userID := range userIDs {
		go func(id uuid.UUID) {
			if err := f.SendNotification(id, title, body, data); err != nil {
				log.Printf("Error sending notification to user %s: %v", id, err)
			}
		}(userID)
	}
}

// Notification types and helper methods
func (f *FCMService) SendBookingRequestNotification(rideOwnerID uuid.UUID, passengerName, rideRoute string, bookingID uuid.UUID) error {
	title := "New Ride Request"
	body := fmt.Sprintf("%s wants to join your ride to %s", passengerName, rideRoute)
	data := map[string]string{
		"type":       "booking_request",
		"booking_id": bookingID.String(),
		"action":     "view_booking",
	}
	return f.SendNotification(rideOwnerID, title, body, data)
}

func (f *FCMService) SendBookingAcceptedNotification(passengerID uuid.UUID, rideRoute string, bookingID uuid.UUID) error {
	title := "Ride Request Accepted! 🎉"
	body := fmt.Sprintf("Your request for the ride to %s has been accepted", rideRoute)
	data := map[string]string{
		"type":       "booking_accepted",
		"booking_id": bookingID.String(),
		"action":     "view_ride",
	}
	return f.SendNotification(passengerID, title, body, data)
}

func (f *FCMService) SendBookingRejectedNotification(passengerID uuid.UUID, rideRoute string, bookingID uuid.UUID) error {
	title := "Ride Request Declined"
	body := fmt.Sprintf("Unfortunately, your request for the ride to %s was declined", rideRoute)
	data := map[string]string{
		"type":       "booking_rejected",
		"booking_id": bookingID.String(),
		"action":     "search_rides",
	}
	return f.SendNotification(passengerID, title, body, data)
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

func (f *FCMService) SendDirectMessageNotification(userID uuid.UUID, senderName, message string) error {
	title := fmt.Sprintf("New message from %s", senderName)
	body := fmt.Sprintf("💬 %s", message)
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	data := map[string]string{
		"type":    "direct_message",
		"action":  "open_dm",
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
