package helpers

import "testing"

func TestCanonicalLocationCoordsVITVelloreAliases(t *testing.T) {
	tests := []string{
		"VIT Vellore",
		"VIT Vellore, India",
		"VIT Vellore Main Gate",
		"Vellore Institute of Technology",
	}

	for _, label := range tests {
		lat, lon, ok := CanonicalLocationCoords(label)
		if !ok {
			t.Fatalf("expected %q to resolve to canonical coords", label)
		}
		if lat != CanonicalVITVelloreLat || lon != CanonicalVITVelloreLon {
			t.Fatalf("unexpected coords for %q: got %.6f, %.6f", label, lat, lon)
		}
	}
}

func TestCanonicalLocationCoordsAvoidsGenericVIT(t *testing.T) {
	if _, _, ok := CanonicalLocationCoords("VIT"); ok {
		t.Fatal("generic VIT should not resolve to VIT Vellore")
	}
	if _, _, ok := CanonicalLocationCoords("VIT Chennai"); ok {
		t.Fatal("VIT Chennai should not resolve to VIT Vellore")
	}
	if _, _, ok := CanonicalLocationCoords("Vellore"); ok {
		t.Fatal("generic Vellore should not resolve to VIT Vellore")
	}
}
