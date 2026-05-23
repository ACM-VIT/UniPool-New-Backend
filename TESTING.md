# Integration tests

This repo ships HTTP-level regression tests covering the core user flows
(ride search, ride create, booking lifecycle, chat send + list, user
profile UPI, public nearby endpoints).

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
go test -count=1 .
```

Without `TEST_DB_URL` set, every integration test calls `t.Skip` so
`go test ./...` stays green on machines without the container.

## What's covered

| File | Endpoint(s) | Regression class |
|---|---|---|
| `rides_search_test.go` | `GET /ride/search` | PostGIS spatial query, primary + fallback path, own-rides filter |
| `rides_create_test.go` | `POST /ride/create` | Auth gate, past-time rejection, host-injection security |
| `bookings_test.go` | `POST /bookings/request`, `PUT /bookings/{accept,reject}/:id` | Booking lifecycle, host-only state transitions |
| `chat_test.go` | `GET /chats/me`, `POST /chat/:ride_id/message` | Chat membership gating, preview shape, ordering |
| `user_profile_test.go` | `PATCH /user/profile`, `GET /user/details` | UPI VPA validation + persistence |
| `public_endpoints_test.go` | `GET /rides/nearby{,-count}`, `GET /institutes{,/search}` | Public PostGIS path, institute listing |

## Adding new tests

The harness lives in `integration_harness_test.go`. Use the existing
fixture helpers (`seedInstitute`, `seedUser`, `seedRide`) and the `do()`
HTTP helper. Authenticated requests pass `asUser(email)` as the headers
arg — the harness's `testAuth` middleware reads
`X-Test-User-Email` and looks up the user the same way the prod
middleware does after Firebase verification.

Each test should call `resetDB(t)` first so it starts from an empty
database. The single shared `*gorm.DB` is reused across tests for
speed; only the row data is truncated between tests, not the schema.

## Why CockroachDB locally (and not Postgres+PostGIS)

The production schema uses CockroachDB-specific DDL (`INDEX ... (col
ASC)` syntax, `STRING` type aliases, `INT8`, etc.) and the bug we're
guarding against was specifically about CockroachDB's stricter spatial
function overload resolution. Running tests against vanilla
Postgres+PostGIS would miss the exact class of regression that
prompted this test infrastructure.
