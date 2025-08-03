package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Ride struct {
	ID string `gorm:"type:uuid;primaryKey"`
}

type Booking struct {
	ID string `gorm:"type:uuid;primaryKey"`
}

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL not set in .env")
	}

	db, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}

	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Booking{}).Error; err != nil {
		log.Fatalf("Failed to delete bookings: %v", err)
	}
	log.Println("✔ All bookings deleted.")

	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Ride{}).Error; err != nil {
		log.Fatalf("Failed to delete rides: %v", err)
	}
	log.Println("✔ All rides deleted.")

	log.Println("✅ Cleanup complete — users table was not touched.")
}
