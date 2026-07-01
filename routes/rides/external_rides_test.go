package rides

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func resetVigoCacheForTest() {
	vigoCacheMu.Lock()
	vigoCache = nil
	vigoCacheTime = time.Time{}
	vigoCacheMu.Unlock()

	vigoRefreshMu.Lock()
	vigoRefreshInFlight = false
	vigoRefreshMu.Unlock()
}

func uintPtr(v uint) *uint {
	return &v
}

func waitForVigoRefreshIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		vigoRefreshMu.Lock()
		inFlight := vigoRefreshInFlight
		vigoRefreshMu.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for vigo cache refresh to finish")
}

func TestFilterExternalRidesRequiresBothEndpointsWhenProvided(t *testing.T) {
	rides := []ExternalRideCard{
		{ID: "both", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
		{ID: "start-only", PickupPoint: "VIT Vellore Main Gate", Destination: "Katpadi Junction"},
		{ID: "end-only", PickupPoint: "Bangalore", Destination: "Chennai Airport"},
		{ID: "blank-start", PickupPoint: "", Destination: "Chennai Airport"},
	}

	got := filterExternalRides(rides, "VIT Vellore", "Chennai")
	if len(got) != 1 || got[0].ID != "both" {
		t.Fatalf("expected only the full route match, got %#v", got)
	}
}

func TestFilterExternalRidesAllowsSingleEndpointSearch(t *testing.T) {
	rides := []ExternalRideCard{
		{ID: "both", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
		{ID: "end-only", PickupPoint: "Bangalore", Destination: "Chennai Airport"},
		{ID: "other", PickupPoint: "Bangalore", Destination: "Katpadi Junction"},
	}

	got := filterExternalRides(rides, "", "Chennai")
	if len(got) != 2 || got[0].ID != "both" || got[1].ID != "end-only" {
		t.Fatalf("expected destination matches for one-sided search, got %#v", got)
	}
}

func TestFetchExternalRidesForSearchUsesFreshCache(t *testing.T) {
	resetVigoCacheForTest()
	t.Cleanup(resetVigoCacheForTest)

	vigoCacheMu.Lock()
	vigoCache = []ExternalRideCard{
		{ID: "both", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
		{ID: "start-only", PickupPoint: "VIT Vellore Main Gate", Destination: "Katpadi Junction"},
	}
	vigoCacheTime = time.Now()
	vigoCacheMu.Unlock()

	got := FetchExternalRidesForSearch(ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
	})
	if len(got) != 1 || got[0].ID != "both" {
		t.Fatalf("expected filtered rides from cache, got %#v", got)
	}
}

func TestFetchExternalRidesForSearchTreatsEmptyCacheAsFresh(t *testing.T) {
	resetVigoCacheForTest()
	t.Cleanup(resetVigoCacheForTest)

	vigoCacheMu.Lock()
	vigoCache = []ExternalRideCard{}
	vigoCacheTime = time.Now()
	vigoCacheMu.Unlock()

	got := FetchExternalRidesForSearch(ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
	})
	if got == nil || len(got) != 0 {
		t.Fatalf("expected empty cached result, got %#v", got)
	}
}

func TestFetchExternalRidesForSearchFetchesColdCacheSynchronously(t *testing.T) {
	resetVigoCacheForTest()

	oldFetch := fetchVigoRides
	fetchVigoRides = func() ([]ExternalRideCard, error) {
		return []ExternalRideCard{
			{ID: "fresh", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
		}, nil
	}
	t.Cleanup(func() {
		fetchVigoRides = oldFetch
		resetVigoCacheForTest()
	})

	got := FetchExternalRidesForSearch(ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
	})
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("expected cold cache to fetch and return fresh rides, got %#v", got)
	}
}

func TestFetchExternalRidesForSearchServesStaleCacheWhileRefreshing(t *testing.T) {
	resetVigoCacheForTest()

	oldFetch := fetchVigoRides
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	released := false
	fetchVigoRides = func() ([]ExternalRideCard, error) {
		close(fetchStarted)
		<-releaseFetch
		return []ExternalRideCard{
			{ID: "fresh", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
		}, nil
	}
	t.Cleanup(func() {
		if !released {
			close(releaseFetch)
		}
		waitForVigoRefreshIdle(t)
		fetchVigoRides = oldFetch
		resetVigoCacheForTest()
	})

	vigoCacheMu.Lock()
	vigoCache = []ExternalRideCard{
		{ID: "stale", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport"},
	}
	vigoCacheTime = time.Now().Add(-vigoCacheTTL - time.Minute)
	vigoCacheMu.Unlock()

	got := FetchExternalRidesForSearch(ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
	})
	if len(got) != 1 || got[0].ID != "stale" {
		t.Fatalf("expected stale cached ride while refresh runs, got %#v", got)
	}

	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("expected stale cache hit to trigger a background refresh")
	}

	close(releaseFetch)
	released = true
	waitForVigoRefreshIdle(t)
}

func TestFilterExternalRidesForSearchAppliesDateSeatsAndPrice(t *testing.T) {
	dateStart := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	dateEnd := dateStart.Add(24*time.Hour - time.Nanosecond)
	rides := []ExternalRideCard{
		{ID: "match", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z", AvailableSeats: 2, TotalPrice: uintPtr(400)},
		{ID: "wrong-date", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-06T10:00:00Z", AvailableSeats: 2, TotalPrice: uintPtr(400)},
		{ID: "too-few-seats", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z", AvailableSeats: 1, TotalPrice: uintPtr(400)},
		{ID: "too-expensive", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z", AvailableSeats: 2, TotalPrice: uintPtr(700)},
		{ID: "unknown-price", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z", AvailableSeats: 2},
	}

	got := filterExternalRidesForSearch(rides, ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
		HasDateFilter: true,
		DateStart:     dateStart,
		DateEnd:       dateEnd,
		MinSeats:      uintPtr(2),
		MaxPrice:      uintPtr(500),
	})
	if len(got) != 1 || got[0].ID != "match" {
		t.Fatalf("expected only the ride matching date, seats, and price, got %#v", got)
	}
}

func TestFilterExternalRidesForSearchOrdersPredictably(t *testing.T) {
	rides := []ExternalRideCard{
		{ID: "c", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T12:00:00Z"},
		{ID: "a", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z"},
		{ID: "b", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z"},
	}

	got := filterExternalRidesForSearch(rides, ExternalRideSearchParams{
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai",
	})
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("expected stable departure/id ordering, got %#v", got)
	}
}

func TestLimitExternalRidesRespectsRemainingBudget(t *testing.T) {
	rides := []ExternalRideCard{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	got := limitExternalRides(rides, 2)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("expected first two rides, got %#v", got)
	}

	got = limitExternalRides(rides, -1)
	if len(got) != 0 {
		t.Fatalf("expected negative budget to return no external rides, got %#v", got)
	}
}

func TestFetchExternalRidesForNearbyFiltersByPickupDistance(t *testing.T) {
	resetVigoCacheForTest()
	t.Cleanup(resetVigoCacheForTest)

	vigoCacheMu.Lock()
	vigoCache = []ExternalRideCard{
		{ID: "vit", PickupPoint: "VIT Vellore Main Gate", Destination: "Chennai Airport", DepartureTime: "2026-07-05T10:00:00Z"},
		{ID: "katpadi", PickupPoint: "Katpadi Railway Station", Destination: "VIT Vellore Main Gate", DepartureTime: "2026-07-05T11:00:00Z"},
		{ID: "chennai", PickupPoint: "Chennai Airport", Destination: "VIT Vellore Main Gate", DepartureTime: "2026-07-05T12:00:00Z"},
		{ID: "unknown", PickupPoint: "Some Unmapped Pickup", Destination: "VIT Vellore Main Gate", DepartureTime: "2026-07-05T13:00:00Z"},
	}
	vigoCacheTime = time.Now()
	vigoCacheMu.Unlock()

	got := FetchExternalRidesForNearby(12.9692, 79.1559, 5000)
	if len(got) != 2 || got[0].ID != "vit" || got[1].ID != "katpadi" {
		t.Fatalf("expected VIT and Katpadi external rides only, got %#v", got)
	}
}

func TestFetchExternalRidesForNearbyRespectsRadius(t *testing.T) {
	resetVigoCacheForTest()
	t.Cleanup(resetVigoCacheForTest)

	vigoCacheMu.Lock()
	vigoCache = []ExternalRideCard{
		{ID: "vit", PickupPoint: "VIT Vellore Main Gate", DepartureTime: "2026-07-05T10:00:00Z"},
		{ID: "katpadi", PickupPoint: "Katpadi Railway Station", DepartureTime: "2026-07-05T11:00:00Z"},
	}
	vigoCacheTime = time.Now()
	vigoCacheMu.Unlock()

	got := FetchExternalRidesForNearby(12.9692, 79.1559, 1000)
	if len(got) != 1 || got[0].ID != "vit" {
		t.Fatalf("expected only the pickup within 1km, got %#v", got)
	}
}

func TestExternalLocationCoordsUsesPreciseLandmarkAliases(t *testing.T) {
	tests := []struct {
		label string
		lat   float64
		lng   float64
	}{
		{label: "VIT Vellore Main Gate", lat: 12.969193, lng: 79.155968},
		{label: "Katpadi Railway Station", lat: 12.972153, lng: 79.137637},
		{label: "New Bus Stand", lat: 12.934693, lng: 79.136977},
		{label: "Vellore Bypass / Near DTDC", lat: 12.931215, lng: 79.134108},
	}

	for _, tt := range tests {
		gotLat, gotLng, ok := externalLocationCoords(tt.label)
		if !ok {
			t.Fatalf("expected %q to resolve to coordinates", tt.label)
		}
		if math.Abs(gotLat-tt.lat) > 0.000001 || math.Abs(gotLng-tt.lng) > 0.000001 {
			t.Fatalf("expected %q to resolve to %f,%f, got %f,%f", tt.label, tt.lat, tt.lng, gotLat, gotLng)
		}
	}
}

func TestExternalLocationCoordsRejectsAmbiguousSingleToken(t *testing.T) {
	if _, _, ok := externalLocationCoords("Vellore"); ok {
		t.Fatal("generic single-token location should not resolve to an arbitrary Vellore landmark")
	}
}

func TestExternalRideAvailableSeatsKeepsExplicitZero(t *testing.T) {
	totalSeats := "4"
	availableSeats := "0"
	fields := map[string]firestoreField{
		"totalSeats":     {IntegerValue: &totalSeats},
		"availableSeats": {IntegerValue: &availableSeats},
		"passengerIds": {
			ArrayValue: &firestoreArrayValue{
				Values: []json.RawMessage{json.RawMessage(`{}`)},
			},
		},
	}

	if got := externalRideAvailableSeats(fields); got != 0 {
		t.Fatalf("expected explicit zero available seats to be preserved, got %d", got)
	}
}

func TestExternalRideAvailableSeatsFallsBackWhenMissing(t *testing.T) {
	totalSeats := "4"
	fields := map[string]firestoreField{
		"totalSeats": {IntegerValue: &totalSeats},
		"passengerIds": {
			ArrayValue: &firestoreArrayValue{
				Values: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)},
			},
		},
	}

	if got := externalRideAvailableSeats(fields); got != 1 {
		t.Fatalf("expected fallback available seats to discount host seat, got %d", got)
	}
}
