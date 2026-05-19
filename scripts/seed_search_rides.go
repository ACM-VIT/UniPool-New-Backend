//go:build seed_search

package main

// Seed a batch of searchable rides into the dev DB so the home-screen
// search flow can be tested end-to-end.
//
// Run with:
//   cd UniPool-New-Backend && go run -tags seed_search scripts/seed_search_rides.go
//
// What it does:
//   1. Loads .env and connects with the same DSN the backend uses.
//   2. Picks a host user (first user that isn't the caller — falls back
//      to creating a synthetic "demo-host@unipool.dev" account if the
//      DB is empty).
//   3. Inserts 10 rides between common SF Bay Area locations across the
//      next 4 days, all in the future, all with seats available.
//
// All ride lat/lng are real SF Bay Area coordinates so the home-screen
// adaptive-radius search returns them.

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type seedUser struct {
	ID    uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name  string
	Email string
}

func (seedUser) TableName() string { return "users" }

type seedRide struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	HostUserID     uuid.UUID
	StartLocation  string
	EndLocation    string
	StartLatitude  *float64
	StartLongitude *float64
	EndLatitude    *float64
	EndLongitude   *float64
	StartTime      time.Time
	TotalSeats     uint
	BookedSeats    uint
	TotalPrice     uint
	IsOngoing      uint
	IsSameGender   uint
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (seedRide) TableName() string { return "rides" }

type seedRow struct {
	StartLocation  string
	EndLocation    string
	StartLat       float64
	StartLng       float64
	EndLat         float64
	EndLng         float64
	OffsetHours    int // hours from now
	TotalSeats     uint
	BookedSeats    uint
	TotalPrice     uint
}

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Println("warn: no .env found in cwd, relying on shell env")
	}

	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		log.Fatal("DB_URL / DATABASE_URL not set")
	}

	db, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	// Resolve host. Pick first user; if none, synth one.
	var host seedUser
	if err := db.Order("created_at asc").First(&host).Error; err != nil {
		log.Println("no users found, creating demo host")
		host = seedUser{
			ID:    uuid.New(),
			Name:  "Demo Host",
			Email: "demo-host@unipool.dev",
		}
		if err := db.Create(&host).Error; err != nil {
			log.Fatalf("could not create demo host: %v", err)
		}
	}
	fmt.Printf("Using host user: %s (%s)\n", host.Name, host.Email)

	now := time.Now()
	rows := []seedRow{
		// SF intra-city
		{"Union Square, SF", "SOMA, SF", 37.7879, -122.4075, 37.7793, -122.4192, 4, 4, 0, 60},
		{"Mission District, SF", "Castro, SF", 37.7599, -122.4148, 37.7609, -122.4350, 6, 3, 0, 80},
		{"Union Square, SF", "Marina District, SF", 37.7879, -122.4075, 37.8030, -122.4378, 8, 4, 1, 90},

		// SF → East Bay
		{"Union Square, SF", "UC Berkeley", 37.7879, -122.4075, 37.8716, -122.2727, 14, 4, 0, 220},
		{"SOMA, SF", "Oakland Downtown", 37.7793, -122.4192, 37.8044, -122.2712, 20, 5, 1, 180},

		// SF → Peninsula
		{"Union Square, SF", "Palo Alto, Stanford", 37.7879, -122.4075, 37.4275, -122.1697, 26, 4, 0, 350},
		{"Mission District, SF", "Mountain View", 37.7599, -122.4148, 37.3861, -122.0839, 32, 4, 1, 400},
		{"Union Square, SF", "SFO Airport", 37.7879, -122.4075, 37.6213, -122.3790, 38, 4, 0, 250},

		// Day-after-tomorrow
		{"Castro, SF", "Berkeley", 37.7609, -122.4350, 37.8716, -122.2727, 52, 3, 0, 240},
		{"SOMA, SF", "Palo Alto, Stanford", 37.7793, -122.4192, 37.4275, -122.1697, 72, 4, 1, 360},
	}

	inserted := 0
	for _, r := range rows {
		startLat := r.StartLat
		startLng := r.StartLng
		endLat := r.EndLat
		endLng := r.EndLng
		ride := seedRide{
			ID:             uuid.New(),
			HostUserID:     host.ID,
			StartLocation:  r.StartLocation,
			EndLocation:    r.EndLocation,
			StartLatitude:  &startLat,
			StartLongitude: &startLng,
			EndLatitude:    &endLat,
			EndLongitude:   &endLng,
			StartTime:      now.Add(time.Duration(r.OffsetHours) * time.Hour),
			TotalSeats:     r.TotalSeats,
			BookedSeats:    r.BookedSeats,
			TotalPrice:     r.TotalPrice,
			IsOngoing:      0,
			IsSameGender:   0,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := db.Create(&ride).Error; err != nil {
			fmt.Printf("  ✗ %s → %s: %v\n", r.StartLocation, r.EndLocation, err)
			continue
		}
		fmt.Printf("  ✓ %s → %s   @ %s   ₹%d   (%d seats free)\n",
			r.StartLocation, r.EndLocation,
			ride.StartTime.Format("Mon 02 Jan 15:04"),
			r.TotalPrice,
			r.TotalSeats-r.BookedSeats,
		)
		inserted++
	}

	fmt.Printf("\nSeeded %d rides under host %s\n", inserted, host.Name)
}
