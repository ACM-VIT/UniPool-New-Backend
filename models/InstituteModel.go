package models

import "github.com/google/uuid"

// Institute represents a university / college whose students share the
// app. One Institute can have many email-domain aliases (a single
// university often has `uni.edu`, `students.uni.edu`, `alum.uni.edu`,
// etc.) — those live in `InstituteDomain` rows pointing at this one.
//
// The split keeps the lookup fast: signup just does a single indexed
// query against `institute_domains.domain`. Adding a new alias is one
// row, not a migration.
type Institute struct {
	BaseModel
	Name    string `gorm:"type:varchar(200);not null" json:"name"`
	Country string `gorm:"type:varchar(80)" json:"country,omitempty"`
}

// InstituteDomain maps a single email domain (e.g. "vitstudent.ac.in")
// to its parent Institute. Domains are stored lowercased and indexed
// uniquely.
type InstituteDomain struct {
	BaseModel
	Domain      string    `gorm:"type:varchar(120);not null;uniqueIndex" json:"domain"`
	InstituteID uuid.UUID `gorm:"type:uuid;not null;index" json:"institute_id"`
	Institute   Institute `gorm:"foreignKey:InstituteID" json:"institute,omitempty"`
}
