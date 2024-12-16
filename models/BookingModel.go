package models

import (
	"gorm.io/gorm"
)

// Booking struct
type Booking struct {
	gorm.Model
	RideID        uint   `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"ride_id" valid:"required~Ride ID is required"`
	Ride          Ride   `gorm:"foreignKey:RideID;references:ID" json:"ride" valid:"-"`
	PassengerID   uint   `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"passenger_id" valid:"required~Passenger ID is required"`
	Passenger     User   `gorm:"foreignKey:PassengerID;references:ID" json:"passenger" valid:"-"`
	RequestStatus string `gorm:"type:varchar(10);not null" json:"request_status" valid:"required~Status is required,in(pending|accepted|rejected)~Status must be accepted or pending or rejected"`
}
