package models

import (
	"time"

	"github.com/google/uuid"
)

// BaseModel is the base model for all models
type BaseModel struct {
	ID        uuid.UUID  `gorm:"primary_key;type:uuid;" json:"id"`
	CreatedAt time.Time  `json:"created_at" sql:"index"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// BeforeCreate will set a UUID rather than numeric ID.
func (base *BaseModel) BeforeCreate() (err error) {
	uuid, err := uuid.NewUUID()
	if err != nil {
		return err
	}
	base.ID = uuid
	return nil
}
