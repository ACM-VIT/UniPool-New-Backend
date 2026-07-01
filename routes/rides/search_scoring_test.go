package rides

import "testing"

func TestScoreTextFitIgnoresGenericAirportWords(t *testing.T) {
	if got := scoreTextFit("Chennai International Airport", "Bangalore Kempegowda International Airport, India", 1); got != 0 {
		t.Fatalf("expected airport-only overlap to score 0, got %f", got)
	}
}

func TestScoreTextFitKeepsSpecificPlaceWords(t *testing.T) {
	if got := scoreTextFit("Chennai International Airport", "Chennai International Airport", 1); got <= 0 {
		t.Fatalf("expected exact airport match to score, got %f", got)
	}

	if got := scoreTextFit("VIT Vellore", "VIT Vellore", 1); got <= 0 {
		t.Fatalf("expected campus match to score, got %f", got)
	}
}
