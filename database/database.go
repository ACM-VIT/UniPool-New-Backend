package database

import (
	"log"
	"os"
	"strconv"
	"strings"

	// "runtime"
	"time"

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

	// Configure GORM. PrepareStmt caches plan-resolved statements
	// across calls, which is the single biggest CockroachDB Cloud win
	// because the wire-protocol parse step is what dominates a fast
	// query's latency.
	//
	// SlowThreshold sits at 300ms intentionally. The app server runs
	// in the same AWS region as the CockroachDB Cloud cluster but
	// over a separate VPC, so a freshly-opened connection
	// (TLS+handshake+pool resolution) routinely lands in the
	// 200-280ms band on the first query of its lifetime. Logging at
	// 100ms was drowning out genuine slow paths under that handshake
	// noise. 300ms keeps signal-to-noise honest while still flagging
	// any real query regression like the PostGIS bug we caught
	// recently.
	config := &gorm.Config{
		PrepareStmt: true,
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             300 * time.Millisecond,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
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
	connMaxLifetimeMinutes := envInt("DB_CONN_MAX_LIFETIME_MINUTES", 30)
	// 15 minutes is intentional: the notification scheduler ticks every
	// 10 minutes and was always coming back to a closed connection,
	// paying TLS+handshake of ~500-700ms on every tick (visible in the
	// slow-query log). With a 15m idle window the scheduler's
	// connection survives across ticks.
	connMaxIdleMinutes := envInt("DB_CONN_MAX_IDLE_MINUTES", 15)

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

	// Schema migrations are managed by goose under `migrations/`. The
	// service binary does NOT run them on boot any more; deploy steps
	// must invoke `unipool-backend migrate` explicitly before bringing
	// new code online. See database/migrate.go for the rationale.
	//
	// We still create performance indexes here on every boot because
	// (a) `CREATE INDEX CONCURRENTLY IF NOT EXISTS` is genuinely
	// idempotent and (b) it lets us add new indexes without a
	// migration round-trip while we're still building. New indexes
	// SHOULD move into proper migrations once the schema settles.
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

	// chat_reads table + its unique indexes are now created by GORM
	// AutoMigrate from the model's `uniqueIndex:` tags (idx_chat_reads_user_ride
	// and idx_chat_reads_user_dm). The old inline CREATE-TABLE-IF-NOT-EXISTS
	// bootstrap is gone — keeping the schema definition in one place
	// (the model) instead of split between model + database.go.

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
