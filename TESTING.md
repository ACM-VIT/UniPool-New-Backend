# Integration tests

HTTP-level regression tests covering the core user flows (ride search,
ride create, booking lifecycle, chat send + list, mark-read membership,
user profile UPI, public nearby endpoints).

The tests exist because of the May 2026 PostGIS `ST_MakePoint` cast bug
that returned empty `/ride/search` results to every user for several
hours before anyone noticed. They sit above the SQL boundary so any
future change that breaks a real query path against a real DB fails
the suite, regardless of which sub-package owns the bug.

## Running locally

Tests require a CockroachDB instance reachable over the postgres wire
protocol (production uses CockroachDB Cloud; these tests run against a
local single-node container so the dialect, type coercion, and spatial
function support all match prod).

```bash
# One-time: start CockroachDB + create the test database.
docker run -d --name unipool-test-crdb \
  -p 26259:26257 -p 8082:8080 \
  cockroachdb/cockroach:v23.2.0 \
  start-single-node --insecure --listen-addr=:26257 --http-addr=:8080
docker exec unipool-test-crdb cockroach sql --insecure \
  -e "CREATE DATABASE unipool_test;"

# Every test run:
export TEST_DB_URL='postgresql://root@127.0.0.1:26259/unipool_test?sslmode=disable'
go test ./tests/integration -count=1
```

Without `TEST_DB_URL` set, every integration test calls `t.Skip` so
`go test ./...` stays green on machines without the container.

## Layout

```
tests/
└── integration/
    ├── harness.go               shared fixtures + helpers (DB, fiber app, seed*, Do, ReadJSON, AsUser)
    ├── rides_search_test.go     GET /ride/search
    ├── rides_create_test.go     POST /ride/create
    ├── bookings_test.go         request -> accept/reject lifecycle
    ├── chat_test.go             /chats/me, /chat/:id/message, mark-read membership
    ├── user_profile_test.go     PATCH /user/profile, GET /user/details
    ├── public_endpoints_test.go /rides/nearby{,-count}, /institutes{,/search}
    └── perf_regressions_test.go locks in the BuildPendingRatings, BuildActiveTripCard,
                                 GetRideDetailsComplete refactors

server/                          extracted from main.go so tests can call
                                 WireRoutes(app, testAuth, testOptionalAuth)
                                 with the same route table prod uses

migsource/                       embedded migrations FS — lives in its own
                                 package because go:embed can't traverse `..`
```

## Adding new tests

Use the existing fixture helpers (`SeedInstitute`, `SeedUser`,
`SeedRide`) and the `Do(...)` HTTP helper from `harness.go`.
Authenticated requests pass `AsUser(email)` as the headers arg — the
harness's `testAuth` middleware reads `X-Test-User-Email` and looks up
the user the same way the prod middleware does after Firebase
verification.

Each test should call `ResetDB(t)` first so it starts from an empty
database. The single shared `*gorm.DB` is reused across tests for
speed; only the row data is truncated between tests, not the schema.

## Why CockroachDB locally (and not Postgres+PostGIS)

The production schema uses CockroachDB-specific DDL (`INDEX ... (col
ASC)` syntax, `STRING` type aliases, `INT8`, etc.) and the bug we're
guarding against was specifically about CockroachDB's stricter spatial
function overload resolution. Running tests against vanilla
Postgres+PostGIS would miss the exact class of regression that
prompted this test infrastructure.
