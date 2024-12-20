package helpers

import (
	"unipool-backend/models"

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
