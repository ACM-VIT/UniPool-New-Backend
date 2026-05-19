package models

import "github.com/google/uuid"

// User struct
type User struct {
	BaseModel
	Name              string `gorm:"type:varchar(100);not null;" json:"name" valid:"required~Name is required,matches(^[a-zA-Z ]+$)~Name must be alphabetic"`
	Email             string `gorm:"type:varchar(100);not null;unique_index" json:"email" valid:"required~Email is required,email~Email is not valid"`
	ProfilePictureURL string `gorm:"type:text" json:"profile_picture_url" valid:"url~URL is not valid"`
	ContactNumber     string `gorm:"type:varchar(20);not null" json:"contact_number" valid:"required~Contact number is required,numeric~Contact number must be numeric"`
	Gender            string `gorm:"type:varchar(10)" json:"gender" valid:"in(male|female|other)~Gender must be male female or other"`
	YOB               uint   `json:"yob" valid:"range(1900|2100)~Year of birth must be between 1900 and 2100"`
	DefaultAddress    string `gorm:"type:varchar(255)" json:"default_address"`
	FCMToken          string `gorm:"type:varchar(500)" json:"fcm_token,omitempty"`
	Platform          string `gorm:"type:varchar(20)" json:"platform,omitempty"`
	DeviceID          string `gorm:"type:varchar(255)" json:"device_id,omitempty"`

	// Institute verification — populated automatically on
	// CreateOrUpdateUser by matching the user's email domain against
	// `institute_domains`. If the domain is known, both fields are
	// set; otherwise InstituteID stays nil and IsEmailVerified=false.
	InstituteID     *uuid.UUID `gorm:"type:uuid;index" json:"institute_id,omitempty"`
	Institute       *Institute `gorm:"foreignKey:InstituteID" json:"institute,omitempty"`
	IsEmailVerified bool       `gorm:"default:false;index" json:"is_email_verified"`

	// Optional UPI VPA the host has saved on their profile. When
	// present, the passenger's post-trip pay sheet builds a
	// `upi://pay?pa=<vpa>&am=<amount>&tn=<note>` deeplink. Empty
	// means we don't show the Pay button — the passenger handles
	// payment off-platform (cash, prior arrangement, etc.).
	UPIVPA string `gorm:"type:varchar(120)" json:"upi_vpa,omitempty"`
}
