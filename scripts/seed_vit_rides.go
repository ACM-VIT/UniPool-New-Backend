//go:build seed_vit

package main

// Seed a realistic batch of VIT Vellore rides to common destinations
// so search / nearby / browse flows have something to show.
//
// Run with:
//   cd UniPool-New-Backend && go run -tags seed_vit scripts/seed_vit_rides.go
//
// What it does:
//   1. Loads .env and connects with the backend's DSN.
//   2. Ensures the VIT institute + its email domains exist (idempotent).
//   3. Creates 6 synthetic VIT student hosts (one per shared domain
//      with `is_email_verified=true` + `institute_id` linked, so they
//      show as verified in the app exactly like a real signup would).
//   4. Inserts ~25 rides across the next 7 days from VIT Vellore to
//      Katpadi, Chennai (city + airport), Bengaluru (city + airport),
//      Tirupati, Pondicherry, Hosur, Salem, and other regional spots.
//      Varied seat counts, varied prices in ₹, varied hosts.
//
// All coordinates are real so the home-screen radius-based search +
// nearby pin clustering pick them up. Prices are rough per-seat splits
// based on real fuel cost / distance.

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

/* -------------------------------------------------------------------
 * Lightweight model duplicates so this script doesn't depend on the
 * full models package (which pulls in fiber/firebase/services and
 * would force a much bigger build). We mirror only the columns we
 * actually write.
 * ----------------------------------------------------------------- */

type seedInstitute struct {
	ID      uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name    string
	Country string
}

func (seedInstitute) TableName() string { return "institutes" }

type seedInstituteDomain struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Domain      string
	InstituteID uuid.UUID
}

func (seedInstituteDomain) TableName() string { return "institute_domains" }

type seedUser struct {
	ID                uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name              string
	Email             string
	ProfilePictureURL string
	ContactNumber     string
	Gender            string
	YOB               uint
	InstituteID       *uuid.UUID
	IsEmailVerified   bool
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
	VehicleInfo    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (seedRide) TableName() string { return "rides" }

/* -------------------------------------------------------------------
 * Data
 * ----------------------------------------------------------------- */

// VIT Vellore main gate — origin for every seeded ride.
const (
	vitName = "VIT Vellore"
	vitLat  = 12.9698
	vitLng  = 79.1559
)

type dest struct {
	Name string
	Lat  float64
	Lng  float64
	// Per-seat ₹ price for this destination — rough fuel-split number.
	PriceINR uint
}

// Destinations from VIT Vellore. Ordered loosely by distance so the
// closer ones get more frequent ride slots in the schedule below.
var destinations = []dest{
	{"Katpadi Junction, Vellore", 12.9716, 79.1374, 50},
	{"Vellore Fort", 12.9192, 79.1300, 70},
	{"CMC Hospital, Vellore", 12.9202, 79.1349, 70},
	{"Ranipet Bus Stand", 12.9242, 79.3338, 130},
	{"Chittoor Bus Stand", 13.2172, 79.1003, 150},
	{"Sholinghur", 13.1166, 79.4222, 200},
	{"Arakkonam Junction", 13.0843, 79.6660, 250},
	{"Krishnagiri Bus Stand", 12.5266, 78.2150, 280},
	{"Tirupati Railway Station", 13.6288, 79.4192, 350},
	{"Chennai Central", 13.0827, 80.2785, 450},
	{"Chennai International Airport (MAA)", 12.9941, 80.1709, 500},
	{"Hosur Bus Stand", 12.7367, 77.8324, 450},
	{"Pondicherry / Puducherry", 11.9416, 79.8083, 550},
	{"Salem New Bus Stand", 11.6643, 78.1460, 600},
	{"Bengaluru City (MG Road)", 12.9716, 77.5946, 700},
	{"Bengaluru Airport (BLR)", 13.1986, 77.7066, 800},
}

type seedHost struct {
	Name    string
	Email   string
	Gender  string
	YOB     uint
	Vehicle string
}

// Six synthetic VIT students. Emails are on real VIT domains so the
// institute matcher would resolve them the same way a live signup
// would; we set institute_id + is_email_verified directly here.
var hosts = []seedHost{
	{"Aarav Sharma", "aarav.sharma2024@vitstudent.ac.in", "male", 2004, "Black Honda City, plate ends 4321"},
	{"Ananya Reddy", "ananya.reddy2023@vitstudent.ac.in", "female", 2003, "White Maruti Baleno, plate ends 7782"},
	{"Rohan Verma", "rohan.verma2024@vitstudent.ac.in", "male", 2004, "Silver Hyundai i20, plate ends 0099"},
	{"Priya Iyer", "priya.iyer2025@vitstudent.ac.in", "female", 2005, "Red Tata Punch, plate ends 1133"},
	{"Karan Mehta", "karan.mehta2024@vitstudent.ac.in", "male", 2004, "Grey Mahindra XUV300, plate ends 6611"},
	{"Sneha Patel", "sneha.patel2024@vitstudent.ac.in", "female", 2004, "Blue Kia Sonet, plate ends 8842"},
}

/* -------------------------------------------------------------------
 * Main
 * ----------------------------------------------------------------- */

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Println("warn: no .env in cwd, relying on shell env")
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

	// --- 1. Ensure VIT institute + domains exist ---
	instituteID := ensureInstitute(db)
	fmt.Printf("Institute: VIT (%s)\n", instituteID)

	// --- 2. Ensure host users exist + are linked to VIT ---
	hostIDs := ensureHosts(db, instituteID)
	fmt.Printf("Hosts: %d ready\n", len(hostIDs))

	// --- 3. Seed rides ---
	inserted := seedRides(db, hostIDs)
	fmt.Printf("\nSeeded %d rides from VIT Vellore\n", inserted)
}

func ensureInstitute(db *gorm.DB) uuid.UUID {
	const name = "Vellore Institute of Technology"
	var inst seedInstitute
	err := db.Where("name = ?", name).First(&inst).Error
	if err != nil {
		inst = seedInstitute{
			ID:      uuid.New(),
			Name:    name,
			Country: "India",
		}
		if err := db.Create(&inst).Error; err != nil {
			log.Fatalf("create institute failed: %v", err)
		}
	}

	domains := []string{
		"vit.ac.in",
		"vitstudent.ac.in",
		"vitap.ac.in",
		"vitbhopal.ac.in",
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		var existing seedInstituteDomain
		if err := db.Where("domain = ?", d).First(&existing).Error; err == nil {
			continue
		}
		_ = db.Create(&seedInstituteDomain{
			ID:          uuid.New(),
			Domain:      d,
			InstituteID: inst.ID,
		}).Error
	}
	return inst.ID
}

func ensureHosts(db *gorm.DB, instituteID uuid.UUID) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(hosts))
	for _, h := range hosts {
		var existing seedUser
		if err := db.Where("email = ?", h.Email).First(&existing).Error; err == nil {
			// Make sure the verified flag + institute link are
			// applied even on already-existing rows; safe to update.
			if existing.InstituteID == nil || !existing.IsEmailVerified {
				_ = db.Model(&seedUser{}).
					Where("id = ?", existing.ID).
					Updates(map[string]any{
						"institute_id":      instituteID,
						"is_email_verified": true,
					}).Error
			}
			ids = append(ids, existing.ID)
			continue
		}

		instID := instituteID
		row := seedUser{
			ID:              uuid.New(),
			Name:            h.Name,
			Email:           h.Email,
			ContactNumber:   randPhone(),
			Gender:          h.Gender,
			YOB:             h.YOB,
			InstituteID:     &instID,
			IsEmailVerified: true,
		}
		if err := db.Create(&row).Error; err != nil {
			log.Printf("  ✗ create host %s: %v", h.Email, err)
			continue
		}
		ids = append(ids, row.ID)
		fmt.Printf("  + host %s (%s)\n", h.Name, h.Email)
	}
	return ids
}

// seedRides builds out a 7-day schedule of rides departing from VIT
// to the destination list. Each destination gets multiple slots
// spread across morning / afternoon / late-evening windows so a user
// browsing at any time finds something fresh.
func seedRides(db *gorm.DB, hostIDs []uuid.UUID) int {
	if len(hostIDs) == 0 {
		log.Fatal("no hosts available — cannot seed rides")
	}
	now := time.Now()
	r := rand.New(rand.NewSource(now.UnixNano()))

	// Departure windows over the next 7 days. Tuple of (day offset, hour of day).
	type slot struct {
		dayOffset int
		hour      int
	}
	slots := []slot{
		// Today (later)
		{0, 18}, {0, 21},
		// Tomorrow
		{1, 6}, {1, 9}, {1, 14}, {1, 17}, {1, 20},
		// Day after
		{2, 7}, {2, 12}, {2, 16}, {2, 19},
		// Day 3-4
		{3, 8}, {3, 18},
		{4, 6}, {4, 17},
		// Weekend window (Fri-evening exodus + Sunday return are the big ones)
		{5, 17}, {5, 19},
		{6, 9}, {6, 18},
	}

	rides := make([]seedRide, 0, len(destinations)*2)
	hostVehicleByID := map[uuid.UUID]string{}
	for i, id := range hostIDs {
		hostVehicleByID[id] = hosts[i].Vehicle
	}

	// For each destination, pick 1-2 slots. Closer destinations
	// (cheaper price → first half of the destinations slice) get
	// more frequent slots since intra-city carpools turn over faster.
	for i, d := range destinations {
		ridesForDest := 1
		if i < len(destinations)/2 {
			ridesForDest = 2
		}
		for k := 0; k < ridesForDest; k++ {
			s := slots[r.Intn(len(slots))]
			depart := time.Date(
				now.Year(), now.Month(), now.Day()+s.dayOffset,
				s.hour, r.Intn(60), 0, 0,
				now.Location(),
			)
			// Skip slots that have already passed today.
			if depart.Before(now) {
				continue
			}

			totalSeats := uint(3 + r.Intn(3)) // 3..5
			bookedSeats := uint(r.Intn(int(totalSeats)))
			hostID := hostIDs[r.Intn(len(hostIDs))]

			startLat := vitLat
			startLng := vitLng
			endLat := d.Lat
			endLng := d.Lng

			rides = append(rides, seedRide{
				ID:             uuid.New(),
				HostUserID:     hostID,
				StartLocation:  vitName,
				EndLocation:    d.Name,
				StartLatitude:  &startLat,
				StartLongitude: &startLng,
				EndLatitude:    &endLat,
				EndLongitude:   &endLng,
				StartTime:      depart,
				TotalSeats:     totalSeats,
				BookedSeats:    bookedSeats,
				TotalPrice:     d.PriceINR,
				IsOngoing:      0,
				IsSameGender:   0,
				VehicleInfo:    hostVehicleByID[hostID],
				CreatedAt:      now,
				UpdatedAt:      now,
			})
		}
	}

	inserted := 0
	for _, ride := range rides {
		if err := db.Create(&ride).Error; err != nil {
			fmt.Printf("  ✗ %s → %s: %v\n", ride.StartLocation, ride.EndLocation, err)
			continue
		}
		fmt.Printf("  ✓ %s → %-44s  @ %s   ₹%-4d  (%d/%d seats)\n",
			ride.StartLocation, ride.EndLocation,
			ride.StartTime.Format("Mon 02 Jan 15:04"),
			ride.TotalPrice,
			ride.TotalSeats-ride.BookedSeats,
			ride.TotalSeats,
		)
		inserted++
	}
	return inserted
}

func randPhone() string {
	const digits = "0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 10)
	b[0] = '9'
	for i := 1; i < 10; i++ {
		b[i] = digits[r.Intn(len(digits))]
	}
	return string(b)
}
