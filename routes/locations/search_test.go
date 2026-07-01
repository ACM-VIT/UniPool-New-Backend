package locations

import "testing"

func TestDedupeAndSortCollapsesNearIdenticalLocations(t *testing.T) {
	results := []LocationResult{
		{
			DisplayName: "VIT Vellore, India",
			Lat:         "12.9692",
			Lon:         "79.1559",
			PlaceID:     "osm_1",
			Name:        "VIT Vellore",
			Source:      "osm",
			Score:       120,
		},
		{
			DisplayName: "VIT Vellore, Vellore, India",
			Lat:         "12.9701",
			Lon:         "79.1562",
			PlaceID:     "curated_vit_vellore",
			Name:        "VIT Vellore",
			Source:      "curated",
			Score:       180,
		},
		{
			DisplayName: "Katpadi Junction, Vellore, India",
			Lat:         "12.9726",
			Lon:         "79.1372",
			PlaceID:     "curated_katpadi",
			Name:        "Katpadi Junction",
			Source:      "curated",
			Score:       100,
		},
	}

	out := dedupeAndSort(results, "vit ve", 10, false, 0, 0)
	if len(out) != 2 {
		t.Fatalf("expected duplicate VIT rows to collapse; got %d rows: %#v", len(out), out)
	}
	if out[0].PlaceID != "curated_vit_vellore" {
		t.Fatalf("expected best VIT row to win; got %q", out[0].PlaceID)
	}
}

func TestDedupeAndSortKeepsSameNameFarApart(t *testing.T) {
	results := []LocationResult{
		{
			DisplayName: "Gandhi Nagar, Vellore, India",
			Lat:         "12.9489",
			Lon:         "79.1376",
			PlaceID:     "vellore_gandhi_nagar",
			Name:        "Gandhi Nagar",
			Score:       120,
		},
		{
			DisplayName: "Gandhi Nagar, Jaipur, India",
			Lat:         "26.9124",
			Lon:         "75.7873",
			PlaceID:     "jaipur_gandhi_nagar",
			Name:        "Gandhi Nagar",
			Score:       110,
		},
	}

	out := dedupeAndSort(results, "gandhi nagar", 10, false, 0, 0)
	if len(out) != 2 {
		t.Fatalf("expected far-apart same-name places to remain separate; got %d rows: %#v", len(out), out)
	}
}

func TestDedupeAndSortCollapsesRideHistoryVariantsForCuratedPlace(t *testing.T) {
	results := []LocationResult{
		{
			DisplayName: "VIT Vellore, India",
			Lat:         "12.9692",
			Lon:         "79.1559",
			PlaceID:     "curated_vellore_vit_vellore",
			Name:        "VIT Vellore",
			Source:      "curated",
			Score:       225,
		},
		{
			DisplayName: "VIT Vellore",
			Lat:         "12.9235541",
			Lon:         "79.1330768",
			PlaceID:     "ride_vit_vellore",
			Name:        "VIT Vellore",
			Source:      "ride_history",
			Score:       185,
		},
		{
			DisplayName: "VIT Vellore, India",
			Lat:         "12.969219",
			Lon:         "79.15592",
			PlaceID:     "ride_vit_vellore_india",
			Name:        "VIT Vellore, India",
			Source:      "ride_history",
			Score:       185,
		},
	}

	out := dedupeAndSort(results, "vit ve", 10, false, 0, 0)
	if len(out) != 1 {
		t.Fatalf("expected VIT variants to collapse to one row; got %d rows: %#v", len(out), out)
	}
	if out[0].PlaceID != "curated_vellore_vit_vellore" {
		t.Fatalf("expected curated VIT row to win; got %q", out[0].PlaceID)
	}
}
