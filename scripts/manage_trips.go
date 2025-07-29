package main

import (
	"bufio"
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

type User struct {
	ID    uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name  string
	Email string
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
	fmt.Print("Enter the number of the user to view/delete/assign trips: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	selectedIdx := 0
	fmt.Sscanf(input, "%d", &selectedIdx)
	if selectedIdx < 1 || selectedIdx > len(users) {
		log.Fatalf("Invalid selection.")
	}
	selectedUser := users[selectedIdx-1]
	fmt.Printf("Selected user: %s (%s)\n", selectedUser.Name, selectedUser.Email)

	// 1. View all trips by user
	var rides []Ride
	if err := db.Where("host_user_id = ?", selectedUser.ID).Find(&rides).Error; err != nil {
		log.Fatalf("Failed to fetch rides: %v", err)
	}
	fmt.Printf("\nTrips hosted by %s:\n", selectedUser.Name)
	for i, r := range rides {
		fmt.Printf("[%d] %s -> %s at %s | ID: %s\n", i+1, r.StartLocation, r.EndLocation, r.StartTime.Format(time.RFC3339), r.ID)
	}
	if len(rides) == 0 {
		fmt.Println("No trips found.")
	}

	// 2. Delete all trips for this user
	fmt.Print("\nDelete all trips for this user? (y/N): ")
	delInput, _ := reader.ReadString('\n')
	delInput = strings.TrimSpace(strings.ToLower(delInput))
	if delInput == "y" {
		if err := db.Where("host_user_id = ?", selectedUser.ID).Delete(&Ride{}).Error; err != nil {
			log.Fatalf("Failed to delete rides: %v", err)
		}
		fmt.Println("All trips deleted.")
	} else {
		fmt.Println("No trips deleted.")
	}

	// 3. Assign users to a single trip
	fmt.Print("\nAssign all users to a new trip together? (y/N): ")
	assignInput, _ := reader.ReadString('\n')
	assignInput = strings.TrimSpace(strings.ToLower(assignInput))
	if assignInput == "y" {
		fmt.Print("Enter start location: ")
		startLoc, _ := reader.ReadString('\n')
		startLoc = strings.TrimSpace(startLoc)
		fmt.Print("Enter end location: ")
		endLoc, _ := reader.ReadString('\n')
		endLoc = strings.TrimSpace(endLoc)
		fmt.Print("Enter total seats: ")
		totalSeatsStr, _ := reader.ReadString('\n')
		totalSeatsStr = strings.TrimSpace(totalSeatsStr)
		totalSeats := uint(4)
		fmt.Sscanf(totalSeatsStr, "%d", &totalSeats)
		trip := Ride{
			ID:            uuid.New(),
			HostUserID:    selectedUser.ID,
			StartLocation: startLoc,
			EndLocation:   endLoc,
			StartTime:     time.Now().Add(10000 * time.Minute),
			TotalSeats:    totalSeats,
			BookedSeats:   0,
			TotalPrice:    100,
			IsOngoing:     1,
			IsSameGender:  0,
		}
		if err := db.Create(&trip).Error; err != nil {
			log.Fatalf("Failed to create trip: %v", err)
		}
		fmt.Printf("Created trip %s\n", trip.ID)
		for _, u := range users {
			booking := Booking{
				ID:          uuid.New(),
				RideID:      trip.ID,
				PassengerID: u.ID,
				RequestStatus: "CONFIRMED",
			}
			if err := db.Create(&booking).Error; err != nil {
				fmt.Printf("Failed to add user %s: %v\n", u.Name, err)
			} else {
				fmt.Printf("Added user %s to trip\n", u.Name)
			}
		}
		fmt.Println("All users assigned to the new trip.")
	} else {
		fmt.Println("No new trip created.")
	}
}
