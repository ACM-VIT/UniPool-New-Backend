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
	// Pinned index name. GORM derives `uni_institute_domains_domain`
	// from `uniqueIndex` without an explicit name, but the derived
	// name has shifted across GORM versions — AutoMigrate then tries
	// to DROP the old constraint and create a new one, fails on the
	// drop, and aborts the migration. Naming it explicitly stops the
	// churn.
	Domain      string    `gorm:"type:varchar(120);not null;uniqueIndex:idx_institute_domains_domain" json:"domain"`
	InstituteID uuid.UUID `gorm:"type:uuid;not null;index" json:"institute_id"`
	Institute   Institute `gorm:"foreignKey:InstituteID" json:"institute,omitempty"`
}
