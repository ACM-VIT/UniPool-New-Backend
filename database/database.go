package database

import (
	"log"
	"os"
	// "runtime"
	"time"

	"unipool-backend/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DbInstance is a representation of the database instance - its only field is the gorm DB instance
type DbInstance struct {
	Db *gorm.DB
}

func GlobalActivationScope(db *gorm.DB) *gorm.DB {
	return db.Where("is_activated = ?", true)
}

var Database DbInstance

func ConnectToDB() {
	connectionString := os.Getenv("DB_URL")

	log.Println("Connecting to database...")
	db, err := gorm.Open(postgres.Open(connectionString), &gorm.Config{
		PrepareStmt: false,
	})
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("Error getting generic DB: %v", err)
	}

	poolSize := 4
	sqlDB.SetMaxOpenConns(poolSize)        
	sqlDB.SetMaxIdleConns(poolSize)        
	sqlDB.SetConnMaxLifetime(15 * time.Minute)   
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)  

	log.Println("Connected to database")

	if os.Getenv("SHOULD_MIGRATE") == "TRUE" {
		log.Println("Running DB Migrations...")

		err = db.AutoMigrate(&models.User{}, &models.Ride{}, &models.Booking{}, &models.UserMetadata{}, &models.Message{})

		if err != nil {
			log.Fatalf("Error running migrations: %v", err)
		}

		log.Println("DB Migrations completed")
	}

	Database = DbInstance{Db: db}
}
