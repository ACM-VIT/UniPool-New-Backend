package database

import (
	"log"
	"os"
	"strings"

	// "runtime"
	"time"

	"unipool-backend/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

	// Configure GORM with better settings
	config := &gorm.Config{
		PrepareStmt: true, // Enable prepared statements for better performance
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold: 100 * time.Millisecond, // Reduced from default 200ms
				LogLevel:      logger.Warn,
				Colorful:      true,
			},
		),
	}

	db, err := gorm.Open(postgres.Open(connectionString), config)
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("Error getting generic DB: %v", err)
	}

	poolSize := 10
	sqlDB.SetMaxOpenConns(poolSize)            
	sqlDB.SetMaxIdleConns(poolSize / 2)        // 5 idle connections
	sqlDB.SetConnMaxLifetime(10 * time.Minute) // Reduced to 10 minutes
	sqlDB.SetConnMaxIdleTime(2 * time.Minute)  // Reduced to 2 minutes

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("Error pinging database: %v", err)
	}

	log.Println("Connected to database and verified connection")

	if os.Getenv("SHOULD_MIGRATE") == "TRUE" {
		log.Println("Running DB Migrations...")

		err = db.AutoMigrate(&models.User{}, &models.Ride{}, &models.Booking{}, &models.UserMetadata{}, &models.Message{})

		if err != nil {
			log.Fatalf("Error running migrations: %v", err)
		}

		log.Println("DB Migrations completed")
	}

	// Create indexes for better query performance
	if err := createIndexes(db); err != nil {
		log.Printf("Warning: Failed to create some indexes: %v", err)
	}

	Database = DbInstance{Db: db}
}

func createIndexes(db *gorm.DB) error {
	log.Println("Creating database indexes for better performance...")
	
	indexes := []struct {
		name string
		sql  string
	}{
		{"idx_users_email", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email)"},
		{"idx_users_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_id ON users(id)"},
		{"idx_users_deleted_at", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_deleted_at ON users(deleted_at)"},

		{"idx_rides_host_user_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_host_user_id ON rides(host_user_id)"},
		{"idx_rides_start_time", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_start_time ON rides(start_time)"},
		{"idx_rides_is_ongoing", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_is_ongoing ON rides(is_ongoing)"},
		{"idx_rides_deleted_at", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_deleted_at ON rides(deleted_at)"},

		{"idx_bookings_ride_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_ride_id ON bookings(ride_id)"},
		{"idx_bookings_passenger_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_passenger_id ON bookings(passenger_id)"},
		{"idx_bookings_request_status", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_request_status ON bookings(request_status)"},
		{"idx_bookings_deleted_at", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_deleted_at ON bookings(deleted_at)"},

		{"idx_bookings_ride_passenger", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_ride_passenger ON bookings(ride_id, passenger_id)"},
		{"idx_rides_host_time", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_host_time ON rides(host_user_id, start_time)"},
	}

	successCount := 0
	for _, index := range indexes {
		log.Printf("Creating index: %s", index.name)
		if err := db.Exec(index.sql).Error; err != nil {
			// Remove CONCURRENTLY and try again if it fails
			fallbackSQL := strings.Replace(index.sql, "CONCURRENTLY ", "", 1)
			log.Printf("Retrying index %s without CONCURRENTLY", index.name)
			if err := db.Exec(fallbackSQL).Error; err != nil {
				log.Printf("Failed to create index %s: %v", index.name, err)
			} else {
				log.Printf("Successfully created index: %s (fallback)", index.name)
				successCount++
			}
		} else {
			log.Printf("Successfully created index: %s", index.name)
			successCount++
		}
	}

	log.Printf("Created %d/%d indexes successfully", successCount, len(indexes))
	return nil
}
