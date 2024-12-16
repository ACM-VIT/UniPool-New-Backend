package models

import (
	"gorm.io/gorm"
)

// UserMetadata struct
type UserMetadata struct {
	gorm.Model
	UserID   uint   `gorm:"not null" json:"user_id" valid:"required~User ID is required"`
	User     User   `gorm:"foreignKey:UserID;references:ID" json:"user" valid:"-"`
	FCMToken string `gorm:"type:text" json:"fcm_token" valid:"required~FCM token is required"`
}
