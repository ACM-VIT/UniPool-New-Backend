package main

// Regression tests for PATCH /user/profile and GET /user/details.
// The UPI VPA field is a payment identifier we expose to other riders
// on the post-trip pay sheet, so a regression here can silently break
// the host's ability to receive payments OR (worse) accept malformed
// VPAs that break the upi:// deeplink on the passenger side.

import (
	"net/http"
	"testing"

	"unipool-backend/models"
)

func TestUpdateProfile_UPIVPA_Valid(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := seedUser(t, db, "Driver", "upi-driver@vitstudent.ac.in", &inst.ID)

	app := setupTestApp(t)
	for _, vpa := range []string{
		"driver@upi",
		"9876543210@paytm",
		"name.dotted@hdfcbank",
	} {
		resp := do(t, app, http.MethodPatch, "/user/profile",
			map[string]any{"upi_vpa": vpa}, asUser(user.Email))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("vpa %q: expected 200, got %d", vpa, resp.StatusCode)
		}
		readJSON(t, resp, nil)
		var refreshed models.User
		db.First(&refreshed, user.ID)
		if refreshed.UPIVPA != vpa {
			t.Errorf("vpa not persisted: got %q want %q", refreshed.UPIVPA, vpa)
		}
	}
}

func TestUpdateProfile_UPIVPA_RejectsMissingAtSign(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := seedUser(t, db, "Driver", "bad-upi@vitstudent.ac.in", &inst.ID)
	app := setupTestApp(t)

	for _, vpa := range []string{"driver-no-at-sign", "1234567890", "just_random"} {
		resp := do(t, app, http.MethodPatch, "/user/profile",
			map[string]any{"upi_vpa": vpa}, asUser(user.Email))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("vpa %q: expected 400, got %d", vpa, resp.StatusCode)
		}
		readJSON(t, resp, nil)
	}
	// And the column should still be empty (no partial writes).
	var refreshed models.User
	db.First(&refreshed, user.ID)
	if refreshed.UPIVPA != "" {
		t.Errorf("expected empty UPI VPA after rejected writes, got %q", refreshed.UPIVPA)
	}
}

func TestUpdateProfile_UPIVPA_EmptyClears(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := seedUser(t, db, "Driver", "clear-upi@vitstudent.ac.in", &inst.ID)

	// Pre-populate so we have something to clear.
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("upi_vpa", "old@upi")

	app := setupTestApp(t)
	resp := do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": ""}, asUser(user.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)
	var refreshed models.User
	db.First(&refreshed, user.ID)
	if refreshed.UPIVPA != "" {
		t.Errorf("expected cleared UPI VPA, got %q", refreshed.UPIVPA)
	}
}

func TestUpdateProfile_AuthRequired(t *testing.T) {
	_ = connectTestDB(t)
	resetDB(t)
	app := setupTestApp(t)
	resp := do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": "x@y"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestUserDetails_ReturnsUPIVPA(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := seedUser(t, db, "Driver", "details-driver@vitstudent.ac.in", &inst.ID)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("upi_vpa", "details@upi")

	app := setupTestApp(t)
	resp := do(t, app, http.MethodGet, "/user/details", nil, asUser(user.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		User struct {
			ID     string `json:"id"`
			Email  string `json:"email"`
			UPIVPA string `json:"upi_vpa"`
		} `json:"user"`
	}
	readJSON(t, resp, &body)
	if body.User.ID != user.ID.String() {
		t.Errorf("id mismatch: got %s want %s", body.User.ID, user.ID)
	}
	if body.User.Email != user.Email {
		t.Errorf("email mismatch: got %s want %s", body.User.Email, user.Email)
	}
	if body.User.UPIVPA != "details@upi" {
		t.Errorf("upi_vpa not returned: got %q want %q", body.User.UPIVPA, "details@upi")
	}
}

func TestUpdateProfile_RejectsTooLongUPI(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := seedUser(t, db, "Driver", "long-upi@vitstudent.ac.in", &inst.ID)
	app := setupTestApp(t)

	// 130-char string with an @, exceeds the 120-char limit.
	tooLong := ""
	for i := 0; i < 130; i++ {
		if i == 50 {
			tooLong += "@"
			continue
		}
		tooLong += "a"
	}
	resp := do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": tooLong}, asUser(user.Email))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for 130-char VPA, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)
}
