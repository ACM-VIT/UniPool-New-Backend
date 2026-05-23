package main

// Integration-test harness for backend regression coverage.
//
// Why these tests exist:
//   The /rides/search endpoint silently 500'd in prod for users hitting
//   the fallback coordinate path because PostGIS rejected
//   ST_MakePoint(decimal, decimal). Nothing in the repo would have
//   caught it. These tests sit above the SQL boundary so any future
//   change that breaks a real query path against a real DB fails the
//   suite, regardless of which sub-package owns the bug.
//
// What this file gives you:
//   - setupTestApp(t) boots a Fiber app wired exactly like prod (same
//     route table via WireRoutes) but with the Firebase auth gate
//     replaced by a header-based test middleware. It points GORM at the
//     CRDB instance behind TEST_DB_URL and applies goose migrations
//     from the embedded FS.
//   - resetDB(t) TRUNCATEs every application table so tests get a
//     clean slate without paying the cost of fresh migrations.
//   - seedInstitute / seedUser / seedRide / seedBooking / seedMessage
//     write the minimum fixtures a test needs without exercising the
//     real handlers (the handlers are what we're testing).
//   - do(t, app, method, path, body, headers) makes a single in-process
//     HTTP request via fiber.Test and returns *http.Response. headers
//     accept asUser(email) for authenticated tests.
//   - asUser(email) returns the header map our test auth middleware
//     understands. The middleware looks up the user by email and
//     populates c.Locals("user") the same way the prod middleware does
//     after Firebase verification.
//
// Skipping:
//   When TEST_DB_URL is empty (the default CI runtime, or any dev
//   machine without a CRDB ready), every integration test skips. Run
//   them locally with:
//     docker run -d --name crdb -p 26259:26257 cockroachdb/cockroach:v23.2.0 \
//         start-single-node --insecure --listen-addr=:26257
//     docker exec crdb cockroach sql --insecure -e "CREATE DATABASE unipool_test;"
//     export TEST_DB_URL='postgresql://root@127.0.0.1:26259/unipool_test?sslmode=disable'
//     go test ./...

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
	"unipool-backend/middleware"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Avoid re-running goose for every test — migrate once per process.
var (
	migrateOnce   sync.Once
	migrateOnceErr error
	sharedDB      *gorm.DB
)

// skipIfNoTestDB short-circuits a test when TEST_DB_URL is unset. The
// caller still gets a clean *testing.T, but with t.Skip already
// applied. Use at the top of every integration test.
func skipIfNoTestDB(t *testing.T) string {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("TEST_DB_URL"))
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping integration test. See integration_harness_test.go for setup.")
	}
	return url
}

// connectTestDB opens (or reuses) the shared gorm connection. Runs
// migrations once per process. Subsequent callers receive the same
// *gorm.DB instance, mirroring how the prod binary uses
// database.Database.Db as a singleton.
func connectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	url := skipIfNoTestDB(t)

	migrateOnce.Do(func() {
		// Bring up a fresh gorm connection. Use the same postgres driver
		// the prod binary uses — CockroachDB speaks the postgres wire
		// protocol so the driver doesn't care it's not "real" Postgres.
		db, err := gorm.Open(postgres.Open(url), &gorm.Config{
			PrepareStmt: true,
		})
		if err != nil {
			migrateOnceErr = err
			return
		}
		sharedDB = db
		database.Database.Db = db

		// Apply embedded migrations via goose. Reuse main.go's
		// migrationsFS so test schema can't drift from prod schema —
		// any new migration the prod binary needs runs here too.
		sqlDB, err := db.DB()
		if err != nil {
			migrateOnceErr = err
			return
		}
		goose.SetBaseFS(migrationsFS)
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

// resetDB wipes every application table so each test starts from
// scratch. TRUNCATE CASCADE handles FK chains in a single call. Order
// doesn't matter under CASCADE but we list the high-traffic tables
// explicitly for clarity.
func resetDB(t *testing.T) {
	t.Helper()
	db := connectTestDB(t)
	// Catch any future tables — read pg_catalog dynamically rather than
	// maintaining a hand-written list that drifts.
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
	// CockroachDB's TRUNCATE accepts multiple tables in one statement
	// and processes CASCADE for FK fanout.
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
// reads X-Test-User-Email and looks the row up in the test DB. If the
// header is missing the request 401s the same way production would.
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
// header is present and resolves to a real user we set the local,
// otherwise we transparently fall through so guests can hit the route.
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

// setupTestApp wires a Fiber app exactly like prod's main(), minus
// Firebase + FCM + websocket initialization. Routes come from the same
// WireRoutes() the prod binary calls, so any new handler registered
// there is automatically reachable from tests.
func setupTestApp(t *testing.T) *fiber.App {
	t.Helper()
	// Touch sharedDB so the connection + migrations are live before any
	// route runs.
	_ = connectTestDB(t)

	app := fiber.New(fiber.Config{
		// Keep the test fiber lean; we don't need the 10MB body limit
		// or 60s timeouts and they only slow down failure modes.
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
	SetupMiddleware(app)

	// Same route registration as prod, but with our header-based test
	// middleware in place of the Firebase variants.
	WireRoutes(app, testAuth, testOptionalAuth)

	// Sanity: production main() uses middleware.Authenticate /
	// OptionalAuthenticate as the canonical functions. We DON'T touch
	// them so other tests that import the package don't surprise-bind.
	_ = middleware.Authenticate
	_ = middleware.OptionalAuthenticate

	return app
}

// asUser returns the header map our test auth middleware accepts.
func asUser(email string) map[string]string {
	return map[string]string{"X-Test-User-Email": email}
}

// do performs an in-process HTTP request against the Fiber app via
// fiber's Test() helper (no listening socket, no port conflicts). The
// returned *http.Response is the standard library type so tests can
// use any helper.
func do(t *testing.T, app *fiber.App, method, path string, body any, headers map[string]string) *http.Response {
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

// readJSON consumes the response body, parses JSON into target, and
// closes the body. Returns the raw bytes for assertion-level debugging
// when the decode succeeds.
func readJSON(t *testing.T, resp *http.Response, target any) []byte {
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

// Fixture helpers ------------------------------------------------------

// seedInstitute inserts an Institute + one InstituteDomain so test
// users with matching emails resolve as verified. Returns the created
// row so tests can attach users / rides to it.
func seedInstitute(t *testing.T, db *gorm.DB, name, country, domain string) models.Institute {
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

// seedUser creates a User row directly. Bypasses the
// /user CreateOrUpdate handler so tests can compose arbitrary fixtures
// without going through a public API contract that might also fail.
func seedUser(t *testing.T, db *gorm.DB, name, email string, instituteID *uuid.UUID) models.User {
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

// RideOpts configures a seeded ride. Zero values pick sensible defaults
// for a typical Vellore-area trip 1h from now with 4 seats.
type RideOpts struct {
	StartLocation  string
	EndLocation    string
	StartLat       *float64
	StartLon       *float64
	EndLat         *float64
	EndLon         *float64
	StartTime      time.Time
	TotalSeats     uint
	BookedSeats    uint
	TotalPrice     uint
}

func seedRide(t *testing.T, db *gorm.DB, host models.User, opts RideOpts) models.Ride {
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

// floatPtr is a tiny helper to keep tests readable when assigning
// optional coordinate fields on RideOpts.
func floatPtr(v float64) *float64 { return &v }
