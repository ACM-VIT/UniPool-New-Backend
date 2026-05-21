package database

import (
	"log"
	"os"
	"strconv"
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

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("Invalid %s=%q, using %d", name, raw, fallback)
		return fallback
	}
	return value
}

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

	maxOpenConns := envInt("DB_MAX_OPEN_CONNS", 30)
	maxIdleConns := envInt("DB_MAX_IDLE_CONNS", 15)
	if maxIdleConns > maxOpenConns {
		maxIdleConns = maxOpenConns
	}
	connMaxLifetimeMinutes := envInt("DB_CONN_MAX_LIFETIME_MINUTES", 10)
	connMaxIdleMinutes := envInt("DB_CONN_MAX_IDLE_MINUTES", 2)

	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(connMaxLifetimeMinutes) * time.Minute)
	sqlDB.SetConnMaxIdleTime(time.Duration(connMaxIdleMinutes) * time.Minute)
	log.Printf(
		"Database pool configured: max_open=%d max_idle=%d max_lifetime=%dm max_idle_time=%dm",
		maxOpenConns,
		maxIdleConns,
		connMaxLifetimeMinutes,
		connMaxIdleMinutes,
	)

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("Error pinging database: %v", err)
	}

	log.Println("Connected to database and verified connection")

	if os.Getenv("SHOULD_MIGRATE") == "TRUE" {
		log.Println("Running DB Migrations...")

		// Institute + InstituteDomain are migrated BEFORE User so the
		// foreign key (`users.institute_id` → `institutes.id`) resolves
		// cleanly on a fresh database.
		err = db.AutoMigrate(
			&models.Institute{},
			&models.InstituteDomain{},
			&models.User{},
			&models.Ride{},
			&models.Booking{},
			&models.UserMetadata{},
			&models.Message{},
			&models.ChatRead{},
			&models.Report{},
			&models.EmailVerification{},
			&models.NotificationPreference{},
			&models.RideRating{},
		)

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

	log.Println("Installing required PostgreSQL extensions...")
	extensions := []struct {
		name string
		sql  string
	}{
		{"postgis", "CREATE EXTENSION IF NOT EXISTS postgis"},
		{"cube", "CREATE EXTENSION IF NOT EXISTS cube"},
		{"earthdistance", "CREATE EXTENSION IF NOT EXISTS earthdistance"},
		{"pg_trgm", "CREATE EXTENSION IF NOT EXISTS pg_trgm"},
	}

	extensionCount := 0
	for _, ext := range extensions {
		log.Printf("Installing extension: %s", ext.name)
		if err := db.Exec(ext.sql).Error; err != nil {
			log.Printf("Warning: Failed to install extension %s: %v", ext.name, err)
		} else {
			log.Printf("Successfully installed extension: %s", ext.name)
			extensionCount++
		}
	}
	log.Printf("Installed %d/%d extensions successfully", extensionCount, len(extensions))

	// Ensure new chat-read table exists even when SHOULD_MIGRATE isn't
	// set. Auto-migrate handles new columns when explicitly enabled;
	// these CREATE ... IF NOT EXISTS statements are a cheap safety
	// net. CockroachDB doesn't support multi-statement prepared queries
	// so we issue them one at a time.
	chatReadsDDL := []string{
		`CREATE TABLE IF NOT EXISTS chat_reads (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at   TIMESTAMPTZ,
			user_id      UUID NOT NULL,
			ride_id      UUID,
			dm_room_id   TEXT,
			last_read_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_reads_user_ride
			ON chat_reads(user_id, ride_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chat_reads_dm
			ON chat_reads(dm_room_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_reads_user_dm
			ON chat_reads(user_id, dm_room_id)`,
	}
	for _, stmt := range chatReadsDDL {
		if err := db.Exec(stmt).Error; err != nil {
			log.Printf("Warning: chat_reads bootstrap DDL failed: %v", err)
		}
	}

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
		{"idx_rides_start_lat_lng", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_start_lat_lng ON rides(start_latitude, start_longitude) WHERE start_latitude IS NOT NULL AND start_longitude IS NOT NULL"},
		{"idx_rides_start_geog", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_start_geog ON rides USING GIST ((ST_SetSRID(ST_MakePoint(start_longitude::float8, start_latitude::float8), 4326)::geography)) WHERE start_latitude IS NOT NULL AND start_longitude IS NOT NULL"},
		{"idx_rides_open_upcoming", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_open_upcoming ON rides(start_time, host_user_id) WHERE deleted_at IS NULL AND is_ongoing = 0"},

		{"idx_bookings_ride_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_ride_id ON bookings(ride_id)"},
		{"idx_bookings_passenger_id", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_passenger_id ON bookings(passenger_id)"},
		{"idx_bookings_request_status", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_request_status ON bookings(request_status)"},
		{"idx_bookings_deleted_at", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_deleted_at ON bookings(deleted_at)"},
		{"idx_bookings_passenger_status", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_passenger_status ON bookings(passenger_id, request_status) WHERE deleted_at IS NULL"},
		{"idx_bookings_ride_status", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_ride_status ON bookings(ride_id, request_status) WHERE deleted_at IS NULL"},

		{"idx_bookings_ride_passenger", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bookings_ride_passenger ON bookings(ride_id, passenger_id)"},
		{"idx_rides_host_time", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rides_host_time ON rides(host_user_id, start_time)"},
		{"idx_ratings_rater_ride_rated", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ratings_rater_ride_rated ON ride_ratings(rater_user_id, ride_id, rated_user_id)"},
		{"idx_ratings_ride_rater", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ratings_ride_rater ON ride_ratings(ride_id, rater_user_id)"},

		// Chat-list & message-history hot paths.
		{"idx_messages_ride_created", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_ride_created ON messages(ride_id, created_at DESC)"},
		{"idx_messages_dm_created", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_dm_created ON messages(dm_room_id, created_at DESC)"},

		// Notification preferences need separate partial unique indexes:
		// Postgres treats NULL values as distinct, so a single
		// (user_id, category, ride_id) unique index does not protect
		// global rows where ride_id IS NULL.
		{"idx_notif_pref_global", "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_notif_pref_global ON notification_preferences(user_id, category) WHERE ride_id IS NULL"},
		{"idx_notif_pref_ride", "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_notif_pref_ride ON notification_preferences(user_id, category, ride_id) WHERE ride_id IS NOT NULL"},
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
