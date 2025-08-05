package helpers

import (
	"unipool-backend/models"

	//"firebase.google.com/go/v4/messaging"
	"github.com/asaskevich/govalidator"
)

func ValidateRide(ride models.Ride) error {
	_, err := govalidator.ValidateStruct(ride)
	return err
}

func ValidateUser(user models.User) error {
	_, err := govalidator.ValidateStruct(user)
	return err
}

func ValidateBooking(booking models.Booking) error {
	_, err := govalidator.ValidateStruct(booking)
	return err
}

func ValidateMessages(messaging models.Message) error {
	_, err := govalidator.ValidateStruct(messaging)
	return err
}
