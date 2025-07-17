package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Ride struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	HostUserID     uuid.UUID
	StartLocation  string
	EndLocation    string
	StartTime      time.Time
	TotalSeats     uint
	BookedSeats    uint
	TotalPrice     uint
	IsOngoing      uint
	IsSameGender   uint
}

type RideInput struct {
	StartLocation  string    `json:"start_location"`
	EndLocation    string    `json:"end_location"`
	StartTime      string    `json:"start_time"`
	TotalSeats     uint      `json:"total_seats"`
	BookedSeats    uint      `json:"booked_seats"`
	TotalPrice     uint      `json:"total_price"`
	IsOngoing      uint      `json:"is_ongoing"`
	IsSameGender   uint      `json:"is_same_gender"`
}

type User struct {
	ID              uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name            string
	Email           string
	ProfilePictureURL string
	ContactNumber   string
	Gender          string
	YOB             uint
}

type Booking struct {
	ID            uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	RideID        uuid.UUID
	PassengerID   uuid.UUID
	RequestStatus string
}

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	DB_URL := os.Getenv("DB_URL")
	if DB_URL == "" {
		log.Fatal("DB_URL not set in .env")
	}
	db, err := gorm.Open(postgres.Open(DB_URL), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	var users []User
	if err := db.Find(&users).Error; err != nil {
		log.Fatalf("Failed to fetch users: %v", err)
	}
	if len(users) == 0 {
		log.Fatal("No users found in the database. Please create a user first.")
	}
	fmt.Println("Available users:")
	for i, u := range users {
		fmt.Printf("[%d] %s (%s)\n", i+1, u.Name, u.Email)
	}
	fmt.Print("Enter the number of the user to use as HostUserID for all rides: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	selectedIdx := 0
	fmt.Sscanf(input, "%d", &selectedIdx)
	if selectedIdx < 1 || selectedIdx > len(users) {
		log.Fatalf("Invalid selection.")
	}
	hostUser := users[selectedIdx-1]
	fmt.Printf("Using user: %s (%s)\n", hostUser.Name, hostUser.Email)

	// change this as per the location of your frontend repo
	file, err := os.Open("../../unipool-new-frontend/dummy-data/Bookings.json")
	if err != nil {
		log.Fatalf("Failed to open dummy data file: %v", err)
	}
	defer file.Close()

	var rideInputs []RideInput
	if err := json.NewDecoder(file).Decode(&rideInputs); err != nil {
		log.Fatalf("Failed to decode dummy data: %v", err)
	}

	for _, input := range rideInputs {
		ride := Ride{
			ID:            uuid.New(),
			HostUserID:    hostUser.ID,
			StartLocation: input.StartLocation,
			EndLocation:   input.EndLocation,
			TotalSeats:    input.TotalSeats,
			BookedSeats:   input.BookedSeats,
			TotalPrice:    input.TotalPrice,
			IsOngoing:     input.IsOngoing,
			IsSameGender:  input.IsSameGender,
		}
		parsedTime, err := time.Parse(time.RFC3339, input.StartTime)
		if err == nil {
			ride.StartTime = parsedTime
		} else {
			ride.StartTime = time.Now()
		}
		if err := db.Create(&ride).Error; err != nil {
			fmt.Printf("Failed to insert ride: %v\n", err)
		} else {
			fmt.Printf("Inserted ride: %v\n", ride.ID)
		}
	}

	fmt.Println("Dummy data seeding complete.")
}
