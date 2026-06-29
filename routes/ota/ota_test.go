package ota

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestManifestAssetURLsIncludeUpdateID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OTA_STORE_DIR", root)
	t.Setenv("OTA_PUBLIC_BASE", "https://updates.example.test")

	writeOTAUpdate(t, root, "2.0.12", "older-update", "old bundle", time.Now().Add(-time.Hour))
	writeOTAUpdate(t, root, "2.0.12", "newer-update", "new bundle", time.Now())

	app := fiber.New()
	app.Get("/api/manifest", ManifestHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/manifest", nil)
	req.Header.Set("expo-platform", "ios")
	req.Header.Set("expo-runtime-version", "2.0.12")
	req.Header.Set("expo-protocol-version", "1")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("manifest request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read manifest body: %v", err)
	}
	text := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", resp.StatusCode, text)
	}
	if !strings.Contains(text, "updateId=newer-update") {
		t.Fatalf("manifest asset URLs should include latest update id; body=%s", text)
	}
	if got := resp.Header.Get("cache-control"); !strings.Contains(got, "no-store") {
		t.Fatalf("manifest should not be cacheable; cache-control=%q", got)
	}
}

func TestAssetHandlerServesSpecificUpdateAfterNewerPublish(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OTA_STORE_DIR", root)

	writeOTAUpdate(t, root, "2.0.12", "older-update", "old bundle", time.Now().Add(-time.Hour))
	writeOTAUpdate(t, root, "2.0.12", "newer-update", "new bundle", time.Now())

	app := fiber.New()
	app.Get("/api/assets", AssetHandler)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?asset=_expo/static/js/ios/entry.hbc&runtimeVersion=2.0.12&platform=ios&updateId=older-update",
		nil,
	)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("asset request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read asset body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("asset status=%d body=%s", resp.StatusCode, body)
	}
	if string(body) != "old bundle" {
		t.Fatalf("expected asset from older update, got %q", body)
	}
	if got := resp.Header.Get("cache-control"); !strings.Contains(got, "immutable") {
		t.Fatalf("updateId-scoped assets should be immutable; cache-control=%q", got)
	}
}

func TestAssetHandlerFallsBackForLegacyRuntimeScopedURLs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OTA_STORE_DIR", root)

	writeOTAUpdate(t, root, "2.0.12", "older-update", "old bundle", time.Now().Add(-time.Hour))
	writeOTAUpdateWithBundlePath(t, root, "2.0.12", "newer-update", "_expo/static/js/ios/new-entry.hbc", "new bundle", time.Now())

	app := fiber.New()
	app.Get("/api/assets", AssetHandler)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?asset=_expo/static/js/ios/entry.hbc&runtimeVersion=2.0.12&platform=ios",
		nil,
	)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("legacy asset request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read asset body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy asset status=%d body=%s", resp.StatusCode, body)
	}
	if string(body) != "old bundle" {
		t.Fatalf("expected legacy fallback to find older asset, got %q", body)
	}
	if got := resp.Header.Get("cache-control"); strings.Contains(got, "immutable") {
		t.Fatalf("legacy runtime-scoped assets should not be immutable; cache-control=%q", got)
	}
}

func writeOTAUpdate(t *testing.T, root, runtime, updateID, bundle string, mod time.Time) {
	t.Helper()
	writeOTAUpdateWithBundlePath(t, root, runtime, updateID, "_expo/static/js/ios/entry.hbc", bundle, mod)
}

func writeOTAUpdateWithBundlePath(t *testing.T, root, runtime, updateID, bundlePath, bundle string, mod time.Time) {
	t.Helper()

	dir := filepath.Join(root, runtime, updateID)
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(bundlePath)), 0o755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundlePath), []byte(bundle), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	metadata := `{
  "version": 1,
  "bundler": "metro",
  "fileMetadata": {
    "ios": {
      "bundle": "` + bundlePath + `",
      "assets": []
    },
    "android": {
      "bundle": "` + bundlePath + `",
      "assets": []
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.Chtimes(dir, mod, mod); err != nil {
		t.Fatalf("chtimes update dir: %v", err)
	}
}
