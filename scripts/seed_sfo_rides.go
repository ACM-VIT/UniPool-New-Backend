//go:build seed_sfo

package main

// Seed a dense batch of SFO-departing rides so the cluster-pin sheet
// has something real to render against. Mirrors `seed_vit_rides.go`
// but in the Bay Area — SFO main terminal as the single pickup coord,
// 15+ destinations across the SF Peninsula / East Bay / South Bay,
// and 4-6 synthetic Stanford / Berkeley student hosts to fan rides
// across.
//
// Run with:
//   cd UniPool-New-Backend && \
//     go run -tags seed_sfo scripts/seed_sfo_rides.go
//
// Why: the previous catalogue was India-centric (VIT Vellore). Testing
// the campus-density problem (50 rides from one coord → cluster sheet)
// is much easier when you're physically in SF and can hand the phone
// to someone with GPS pointing at the actual airport.

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
 * Lightweight model duplicates — same idiom as seed_vit_rides.go to
 * avoid pulling fiber/firebase/services into the build.
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

// SFO main terminal — single pickup coord for every seeded ride. Same
// coord on purpose so they all fall into one map cluster, which is the
// whole point of this seed batch.
const (
	sfoName = "San Francisco International Airport (SFO)"
	sfoLat  = 37.6213
	sfoLng  = -122.3790
)

type dest struct {
	Name     string
	Lat      float64
	Lng      float64
	PriceINR uint
	// How many rides to seed to this destination. Bumped on common
	// commuter destinations (Caltrain, Powell BART, Stanford) so the
	// cluster sheet has a realistic mix.
	Count int
}

var destinations = []dest{
	{"Powell Street BART Station, San Francisco", 37.7847, -122.4078, 600, 4},
	{"San Francisco Caltrain Station, San Francisco", 37.7766, -122.3946, 550, 3},
	{"Stanford University, Stanford", 37.4275, -122.1697, 800, 3},
	{"Palo Alto Caltrain Station, Palo Alto", 37.4435, -122.1639, 750, 2},
	{"Mountain View Caltrain Station, Mountain View", 37.3950, -122.0763, 850, 2},
	{"UC Berkeley, Berkeley", 37.8719, -122.2585, 1000, 2},
	{"Oakland City Center, Oakland", 37.8044, -122.2712, 800, 1},
	{"San Jose Diridon Station, San Jose", 37.3294, -121.9027, 1100, 2},
	{"Fisherman's Wharf, San Francisco", 37.8080, -122.4177, 700, 1},
	{"Embarcadero Station, San Francisco", 37.7955, -122.3937, 650, 1},
	{"Mission District, San Francisco", 37.7599, -122.4148, 600, 1},
	{"Marina District, San Francisco", 37.8021, -122.4368, 700, 1},
	{"Cupertino, Cupertino", 37.3230, -122.0322, 950, 1},
	{"Sausalito, Sausalito", 37.8590, -122.4852, 850, 1},
	{"San Mateo Caltrain Station, San Mateo", 37.5683, -122.3239, 450, 1},
}

type seedHost struct {
	Name    string
	Email   string
	Gender  string
	YOB     uint
	Vehicle string
}

// Synthetic Bay Area student hosts. Emails on real Stanford / Berkeley
// domains so the institute matcher resolves them the same way a real
// signup would.
var hosts = []seedHost{
	{"Alex Chen", "alex.chen@stanford.edu", "male", 2003, "White Tesla Model 3, plate 8XYL233"},
	{"Sophia Martinez", "sophia.martinez@stanford.edu", "female", 2004, "Blue Honda Civic, plate 9KFM772"},
	{"Jordan Lee", "jordan.lee@berkeley.edu", "male", 2002, "Silver Toyota Corolla, plate 6AKQ419"},
	{"Maya Patel", "maya.patel@berkeley.edu", "female", 2003, "Red Subaru Crosstrek, plate 4BVR086"},
	{"Ethan Rivera", "ethan.rivera@stanford.edu", "male", 2004, "Grey Hyundai Ioniq 5, plate 7CFL301"},
	{"Aisha Khan", "aisha.khan@berkeley.edu", "female", 2003, "Black Mazda CX-5, plate 5DJP928"},
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

	stanfordID := ensureInstitute(db, "Stanford University", []string{"stanford.edu"})
	berkeleyID := ensureInstitute(db, "University of California, Berkeley", []string{"berkeley.edu"})
	fmt.Printf("Institutes: stanford=%s berkeley=%s\n", stanfordID, berkeleyID)

	hostIDs := ensureHosts(db, stanfordID, berkeleyID)
	fmt.Printf("Hosts: %d ready\n", len(hostIDs))

	inserted := seedRides(db, hostIDs)
	fmt.Printf("\nSeeded %d rides departing %s\n", inserted, sfoName)
}

func ensureInstitute(db *gorm.DB, name string, domains []string) uuid.UUID {
	var inst seedInstitute
	err := db.Where("name = ?", name).First(&inst).Error
	if err != nil {
		inst = seedInstitute{
			ID:      uuid.New(),
			Name:    name,
			Country: "United States",
		}
		if err := db.Create(&inst).Error; err != nil {
			log.Fatalf("create institute %s failed: %v", name, err)
		}
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

func ensureHosts(db *gorm.DB, stanfordID, berkeleyID uuid.UUID) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(hosts))
	for _, h := range hosts {
		var existing seedUser
		if err := db.Where("email = ?", h.Email).First(&existing).Error; err == nil {
			// Backfill institute link + verified flag on already-existing
			// rows so re-runs converge to the right state.
			instID := stanfordID
			if strings.HasSuffix(strings.ToLower(h.Email), "berkeley.edu") {
				instID = berkeleyID
			}
			if existing.InstituteID == nil || !existing.IsEmailVerified || existing.Gender == "" {
				_ = db.Model(&seedUser{}).
					Where("id = ?", existing.ID).
					Updates(map[string]any{
						"institute_id":      instID,
						"is_email_verified": true,
						"gender":            h.Gender,
					}).Error
			}
			ids = append(ids, existing.ID)
			continue
		}

		instID := stanfordID
		if strings.HasSuffix(strings.ToLower(h.Email), "berkeley.edu") {
			instID = berkeleyID
		}
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

// seedRides walks the destination list and creates `dest.Count` rides
// per destination, spread across the next 5 days at common commuter
// hours. Every ride departs from the SFO main terminal coord so they
// all land in the same map cluster.
func seedRides(db *gorm.DB, hostIDs []uuid.UUID) int {
	if len(hostIDs) == 0 {
		log.Fatal("no hosts available — cannot seed rides")
	}
	now := time.Now()
	r := rand.New(rand.NewSource(now.UnixNano()))

	type slot struct {
		dayOffset int
		hour      int
	}
	// Airport-flavoured slots — early morning red-eye landings, mid-
	// day arrivals, evening hops. Spread across 5 days so the cluster
	// has a depth of departures rather than 15 rides all in 2 hours.
	slots := []slot{
		{0, 9}, {0, 14}, {0, 19},
		{1, 6}, {1, 11}, {1, 16}, {1, 21},
		{2, 8}, {2, 13}, {2, 18},
		{3, 10}, {3, 17},
		{4, 7}, {4, 15}, {4, 20},
	}

	hostVehicleByID := map[uuid.UUID]string{}
	for i, id := range hostIDs {
		hostVehicleByID[id] = hosts[i%len(hosts)].Vehicle
	}

	rides := make([]seedRide, 0, 32)
	for _, d := range destinations {
		for k := 0; k < d.Count; k++ {
			s := slots[r.Intn(len(slots))]
			depart := time.Date(
				now.Year(), now.Month(), now.Day()+s.dayOffset,
				s.hour, r.Intn(60), 0, 0,
				now.Location(),
			)
			if depart.Before(now.Add(15 * time.Minute)) {
				// Skip already-past or about-to-leave slots so the
				// catalogue stays in the bookable future.
				continue
			}

			totalSeats := uint(3 + r.Intn(3))      // 3..5
			bookedSeats := uint(r.Intn(int(totalSeats)))
			hostID := hostIDs[r.Intn(len(hostIDs))]

			startLat := sfoLat
			startLng := sfoLng
			endLat := d.Lat
			endLng := d.Lng

			rides = append(rides, seedRide{
				ID:             uuid.New(),
				HostUserID:     hostID,
				StartLocation:  sfoName,
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
		fmt.Printf("  ✓ %s → %-50s  @ %s   ₹%-4d  (%d/%d seats)\n",
			"SFO", ride.EndLocation,
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
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const digits = "0123456789"
	b := make([]byte, 10)
	b[0] = '4' // pretend it's a US area code starting digit
	for i := 1; i < 10; i++ {
		b[i] = digits[r.Intn(len(digits))]
	}
	return string(b)
}
