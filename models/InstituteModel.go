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
	// `index:..,unique` instead of `uniqueIndex:..`. GORM's column
	// migrator treats a `uniqueIndex` tag as also implying a column
	// UNIQUE attribute, and on every AutoMigrate run it unconditionally
	// emits a DROP for the default-convention constraint name
	// `uni_institute_domains_domain`. On the first run that drop
	// succeeds (the legacy constraint was real); on every restart
	// after it fails with SQLSTATE 42704 and aborts the whole
	// migration. Declaring the index via `index:..,unique` keeps the
	// uniqueness without tripping the column-unique cleanup path.
	Domain      string    `gorm:"type:varchar(120);not null;index:idx_institute_domains_domain,unique" json:"domain"`
	InstituteID uuid.UUID `gorm:"type:uuid;not null;index" json:"institute_id"`
	Institute   Institute `gorm:"foreignKey:InstituteID" json:"institute,omitempty"`
}
