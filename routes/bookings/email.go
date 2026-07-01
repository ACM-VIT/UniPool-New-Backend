package bookings

import (
	"log"

	"unipool-backend/helpers"

	"github.com/google/uuid"
)

func sendBookingEmailIfAllowed(recipientID uuid.UUID, params helpers.BookingEmailParams) {
	if recipientID == uuid.Nil || params.ToEmail == "" {
		return
	}
	allowed, err := helpers.IsNotificationAllowed(recipientID, helpers.NotifBookingEmails, uuid.Nil)
	if err != nil {
		log.Printf("booking email preference lookup user=%s kind=%s: %v", recipientID, params.Kind, err)
		allowed = true
	}
	if !allowed {
		return
	}
	if err := helpers.SendBookingEmail(params); err != nil {
		log.Printf("booking email send user=%s kind=%s: %v", recipientID, params.Kind, err)
	}
}
