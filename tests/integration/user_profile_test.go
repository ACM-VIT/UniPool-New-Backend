package integration

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
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "upi-driver@vitstudent.ac.in", &inst.ID)

	app := SetupTestApp(t)
	for _, vpa := range []string{
		"driver@upi",
		"9876543210@paytm",
		"name.dotted@hdfcbank",
	} {
		resp := Do(t, app, http.MethodPatch, "/user/profile",
			map[string]any{"upi_vpa": vpa}, AsUser(user.Email))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("vpa %q: expected 200, got %d", vpa, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
		var refreshed models.User
		db.First(&refreshed, user.ID)
		if refreshed.UPIVPA != vpa {
			t.Errorf("vpa not persisted: got %q want %q", refreshed.UPIVPA, vpa)
		}
	}
}

func TestUpdateProfile_UPIVPA_RejectsMissingAtSign(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "bad-upi@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	for _, vpa := range []string{"driver-no-at-sign", "1234567890", "just_random"} {
		resp := Do(t, app, http.MethodPatch, "/user/profile",
			map[string]any{"upi_vpa": vpa}, AsUser(user.Email))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("vpa %q: expected 400, got %d", vpa, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
	// And the column should still be empty (no partial writes).
	var refreshed models.User
	db.First(&refreshed, user.ID)
	if refreshed.UPIVPA != "" {
		t.Errorf("expected empty UPI VPA after rejected writes, got %q", refreshed.UPIVPA)
	}
}

func TestUpdateProfile_UPIVPA_EmptyClears(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "clear-upi@vitstudent.ac.in", &inst.ID)

	// Pre-populate so we have something to clear.
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("upi_vpa", "old@upi")

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": ""}, AsUser(user.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
	var refreshed models.User
	db.First(&refreshed, user.ID)
	if refreshed.UPIVPA != "" {
		t.Errorf("expected cleared UPI VPA, got %q", refreshed.UPIVPA)
	}
}

func TestUpdateProfile_DetailsValidAndClear(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "details-edit@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	resp := Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"gender": "Female", "yob": 2005}, AsUser(user.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
	var refreshed models.User
	db.First(&refreshed, user.ID)
	if refreshed.Gender != "Female" || refreshed.YOB != 2005 {
		t.Fatalf("details not persisted: gender=%q yob=%d", refreshed.Gender, refreshed.YOB)
	}

	resp = Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"gender": "", "yob": 0}, AsUser(user.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 clearing details, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
	db.First(&refreshed, user.ID)
	if refreshed.Gender != "" || refreshed.YOB != 0 {
		t.Fatalf("details not cleared: gender=%q yob=%d", refreshed.Gender, refreshed.YOB)
	}
}

func TestUpdateProfile_DetailsRejectInvalid(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "details-invalid@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	resp := Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"gender": "robot"}, AsUser(user.Email))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid gender, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	resp = Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"yob": 1800}, AsUser(user.Email))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid yob, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestUpdateProfile_AuthRequired(t *testing.T) {
	_ = ConnectTestDB(t)
	ResetDB(t)
	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": "x@y"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestUserDetails_ReturnsUPIVPA(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "details-driver@vitstudent.ac.in", &inst.ID)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("upi_vpa", "details@upi")

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/details", nil, AsUser(user.Email))
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
	ReadJSON(t, resp, &body)
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
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	user := SeedUser(t, db, "Driver", "long-upi@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	// 130-char string with an @, exceeds the 120-char limit.
	tooLong := ""
	for i := 0; i < 130; i++ {
		if i == 50 {
			tooLong += "@"
			continue
		}
		tooLong += "a"
	}
	resp := Do(t, app, http.MethodPatch, "/user/profile",
		map[string]any{"upi_vpa": tooLong}, AsUser(user.Email))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for 130-char VPA, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}
