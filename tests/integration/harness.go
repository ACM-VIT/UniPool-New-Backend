// Package integration is the external test package for backend
// regression coverage. Lives outside the main module's root so tests
// can be `go test ./tests/integration` without polluting the build of
// the prod binary, and so all symbols are imported from real
// packages (no `package main` trickery).
//
// Why these tests exist:
//   The /rides/search endpoint silently 500'd in prod for users hitting
//   the fallback coordinate path because PostGIS rejected
//   ST_MakePoint(decimal, decimal). Nothing in the repo would have
//   caught it. These tests sit above the SQL boundary so any future
//   change that breaks a real query path against a real DB fails the
//   suite, regardless of which sub-package owns the bug.
//
// Skipping:
//   When TEST_DB_URL is empty (CI runtime without a CRDB ready), every
//   integration test calls t.Skip. Run them locally with:
//     docker run -d --name crdb -p 26259:26257 cockroachdb/cockroach:v23.2.0 \
//         start-single-node --insecure --listen-addr=:26257
//     docker exec crdb cockroach sql --insecure -e "CREATE DATABASE unipool_test;"
//     export TEST_DB_URL='postgresql://root@127.0.0.1:26259/unipool_test?sslmode=disable'
//     go test ./tests/integration
package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"unipool-backend/database"
	"unipool-backend/migsource"
	"unipool-backend/models"
	"unipool-backend/server"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Avoid re-running goose for every test — migrate once per process.
var (
	migrateOnce    sync.Once
	migrateOnceErr error
	sharedDB       *gorm.DB
)

// SkipIfNoTestDB short-circuits a test when TEST_DB_URL is unset.
func SkipIfNoTestDB(t *testing.T) string {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("TEST_DB_URL"))
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping integration test. See tests/integration/harness.go for setup.")
	}
	return url
}

// ConnectTestDB opens (or reuses) the shared gorm connection. Runs
// migrations once per process. Subsequent callers receive the same
// *gorm.DB instance, mirroring how the prod binary uses
// database.Database.Db as a singleton.
func ConnectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	url := SkipIfNoTestDB(t)

	migrateOnce.Do(func() {
		db, err := gorm.Open(postgres.Open(url), &gorm.Config{
			PrepareStmt: true,
		})
		if err != nil {
			migrateOnceErr = err
			return
		}
		sharedDB = db
		database.Database.Db = db

		sqlDB, err := db.DB()
		if err != nil {
			migrateOnceErr = err
			return
		}
		goose.SetBaseFS(migsource.FS)
		if err := goose.SetDialect("postgres"); err != nil {
			migrateOnceErr = err
			return
		}
		if err := goose.Up(sqlDB, "migrations"); err != nil {
			migrateOnceErr = err
			return
		}
	})
	if migrateOnceErr != nil {
		t.Fatalf("test DB setup failed: %v", migrateOnceErr)
	}
	return sharedDB
}

// ResetDB wipes every application table so each test starts from
// scratch. TRUNCATE CASCADE handles FK chains in a single statement.
// The list is built dynamically so a future migration that adds a
// table doesn't silently leak rows between tests.
func ResetDB(t *testing.T) {
	t.Helper()
	db := ConnectTestDB(t)
	var tables []string
	if err := db.Raw(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_type = 'BASE TABLE'
		  AND table_name NOT LIKE 'goose_%'
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	if len(tables) == 0 {
		return
	}
	quoted := make([]string, 0, len(tables))
	for _, tab := range tables {
		quoted = append(quoted, `"`+tab+`"`)
	}
	stmt := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " CASCADE"
	if err := db.Exec(stmt).Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// testAuth is the test-only auth middleware. It substitutes for
// middleware.Authenticate — instead of verifying a Firebase JWT it
// reads X-Test-User-Email and looks the row up in the test DB.
func testAuth(c *fiber.Ctx) error {
	email := strings.TrimSpace(c.Get("X-Test-User-Email"))
	if email == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "test auth header missing",
		})
	}
	var user models.User
	if err := sharedDB.Where("email = ?", email).First(&user).Error; err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "test user not found: " + email,
		})
	}
	c.Locals("user", user)
	return c.Next()
}

// testOptionalAuth mirrors middleware.OptionalAuthenticate: if the
// header resolves to a real user set the local, otherwise fall
// through so guests can hit the route.
func testOptionalAuth(c *fiber.Ctx) error {
	email := strings.TrimSpace(c.Get("X-Test-User-Email"))
	if email == "" {
		return c.Next()
	}
	var user models.User
	if err := sharedDB.Where("email = ?", email).First(&user).Error; err == nil {
		c.Locals("user", user)
	}
	return c.Next()
}

// SetupTestApp wires a Fiber app exactly like prod main(), minus
// Firebase + FCM + websocket initialization. Routes come from the
// same server.WireRoutes the prod binary calls, so any new handler
// registered there is automatically reachable from tests.
func SetupTestApp(t *testing.T) *fiber.App {
	t.Helper()
	_ = ConnectTestDB(t)

	app := fiber.New(fiber.Config{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		BodyLimit:    2 * 1024 * 1024,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"error": err.Error(),
			})
		},
	})
	server.SetupMiddleware(app)
	server.WireRoutes(app, testAuth, testOptionalAuth)
	return app
}

// AsUser returns the header map our test auth middleware accepts.
func AsUser(email string) map[string]string {
	return map[string]string{"X-Test-User-Email": email}
}

// Do performs an in-process HTTP request against the Fiber app via
// fiber's Test() helper (no listening socket, no port conflicts).
func Do(t *testing.T, app *fiber.App, method, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, "http://test.local"+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, int((30 * time.Second).Milliseconds()))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

// ReadJSON consumes the response body, parses JSON into target, and
// closes the body. Returns the raw bytes for assertion-level
// debugging when the decode succeeds.
func ReadJSON(t *testing.T, resp *http.Response, target any) []byte {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if target != nil {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("decode JSON (%d bytes): %v\nbody: %s", len(raw), err, string(raw))
		}
	}
	return raw
}

// SeedInstitute inserts an Institute + one InstituteDomain.
func SeedInstitute(t *testing.T, db *gorm.DB, name, country, domain string) models.Institute {
	t.Helper()
	inst := models.Institute{Name: name, Country: country}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatalf("seed institute: %v", err)
	}
	dom := models.InstituteDomain{Domain: strings.ToLower(domain), InstituteID: inst.ID}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("seed institute domain: %v", err)
	}
	return inst
}

// SeedUser creates a User row directly. Bypasses the /user
// CreateOrUpdate handler so tests can compose arbitrary fixtures
// without going through a public API contract that might also fail.
func SeedUser(t *testing.T, db *gorm.DB, name, email string, instituteID *uuid.UUID) models.User {
	t.Helper()
	user := models.User{
		Name:            name,
		Email:           email,
		ContactNumber:   "9999999999",
		Gender:          "other",
		YOB:             2000,
		InstituteID:     instituteID,
		IsEmailVerified: instituteID != nil,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user (%s): %v", email, err)
	}
	return user
}

// RideOpts configures a seeded ride. Zero values pick sensible
// defaults for a typical Vellore-area trip 1h from now with 4 seats.
type RideOpts struct {
	StartLocation string
	EndLocation   string
	StartLat      *float64
	StartLon      *float64
	EndLat        *float64
	EndLon        *float64
	StartTime     time.Time
	TotalSeats    uint
	BookedSeats   uint
	TotalPrice    uint
}

// SeedRide inserts a Ride row directly. Same bypass posture as
// SeedUser — composes fixtures without depending on the create-ride
// handler.
func SeedRide(t *testing.T, db *gorm.DB, host models.User, opts RideOpts) models.Ride {
	t.Helper()
	if opts.StartTime.IsZero() {
		opts.StartTime = time.Now().Add(time.Hour).UTC()
	}
	if opts.TotalSeats == 0 {
		opts.TotalSeats = 4
	}
	if opts.TotalPrice == 0 {
		opts.TotalPrice = 100
	}
	if opts.StartLocation == "" {
		opts.StartLocation = "VIT Vellore Main Gate"
	}
	if opts.EndLocation == "" {
		opts.EndLocation = "Katpadi Junction"
	}
	ride := models.Ride{
		HostUserID:     host.ID,
		StartLocation:  opts.StartLocation,
		EndLocation:    opts.EndLocation,
		StartLatitude:  opts.StartLat,
		StartLongitude: opts.StartLon,
		EndLatitude:    opts.EndLat,
		EndLongitude:   opts.EndLon,
		StartTime:      opts.StartTime,
		TotalSeats:     opts.TotalSeats,
		BookedSeats:    opts.BookedSeats,
		TotalPrice:     opts.TotalPrice,
	}
	if err := db.Create(&ride).Error; err != nil {
		t.Fatalf("seed ride: %v", err)
	}
	return ride
}

// FloatPtr is a tiny helper to keep tests readable when assigning
// optional coordinate fields on RideOpts.
func FloatPtr(v float64) *float64 { return &v }
