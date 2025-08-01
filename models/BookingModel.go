package models

import "github.com/google/uuid"

// Booking struct
type Booking struct {
	BaseModel
	RideID        uuid.UUID `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"ride_id" valid:"required~Ride ID is required"`
	Ride          Ride      `gorm:"foreignKey:RideID;references:ID;constraint:OnDelete:CASCADE" json:"ride" valid:"-"`
	PassengerID   uuid.UUID `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"passenger_id" valid:"required~Passenger ID is required"`
	Passenger     User      `gorm:"foreignKey:PassengerID;references:ID;constraint:OnDelete:SET NULL" json:"passenger" valid:"-"`
	RequestStatus string    `gorm:"type:varchar(10);not null" json:"request_status" valid:"required~Status is required,in(pending|accepted|rejected)~Status must be accepted or pending or rejected"`
}
