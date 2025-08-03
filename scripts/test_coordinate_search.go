package main

import (
	"fmt"
	"log"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Printf("Error loading .env file: %v", err)
		return
	}

	database.ConnectToDB()

	fmt.Println("=== Testing Distance Calculations ===")
	
	chennaiLat := 13.0827
	chennaiLon := 80.2707

	velloreLat := 12.9165
	velloreLon := 79.1325
	
	distance := helpers.CalculateDistance(chennaiLat, chennaiLon, velloreLat, velloreLon)
	fmt.Printf("Distance between Chennai and Vellore: %.2f km\n", distance)
	
	fmt.Println("\n=== Testing Coordinate Validation ===")
	validLat := &chennaiLat
	validLon := &chennaiLon
	fmt.Printf("Valid coordinates check: %v\n", helpers.AreCoordinatesValid(validLat, validLon))
	
	var nilLat *float64
	fmt.Printf("Nil coordinates check: %v\n", helpers.AreCoordinatesValid(nilLat, validLon))
	
	fmt.Println("\n=== Testing Ride Creation with Coordinates ===")
	
	var existingRides []models.Ride
	result := database.Database.Db.Limit(5).Find(&existingRides)
	if result.Error != nil {
		log.Printf("Error fetching existing rides: %v", result.Error)
	} else {
		fmt.Printf("Found %d existing rides in database\n", len(existingRides))
		
		for i, ride := range existingRides {
			fmt.Printf("Ride %d: %s -> %s", i+1, ride.StartLocation, ride.EndLocation)
			if helpers.AreCoordinatesValid(ride.StartLatitude, ride.StartLongitude) {
				fmt.Printf(" (%.4f, %.4f)", *ride.StartLatitude, *ride.StartLongitude)
			} else {
				fmt.Printf(" (no coordinates)")
			}
			if helpers.AreCoordinatesValid(ride.EndLatitude, ride.EndLongitude) {
				fmt.Printf(" -> (%.4f, %.4f)", *ride.EndLatitude, *ride.EndLongitude)
			} else {
				fmt.Printf(" -> (no coordinates)")
			}
			fmt.Println()
		}
	}
	
	fmt.Println("\n=== Test Complete ===")
}
