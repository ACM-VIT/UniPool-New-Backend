package models

// Institute represents a university / college whose students share the
// app. Domain is the email domain (e.g. "vitstudent.ac.in") used to
// verify student affiliation at signup.
type Institute struct {
	BaseModel
	Name    string  `gorm:"type:varchar(200);not null" json:"name"`
	Country string  `gorm:"type:varchar(80)" json:"country,omitempty"`
	Domain  *string `gorm:"type:varchar(120);uniqueIndex:idx_institutes_domain" json:"domain,omitempty"`
}
