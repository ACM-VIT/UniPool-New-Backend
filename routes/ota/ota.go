// Package ota implements the Expo Updates protocol v1 against a
// file-backed update store, plus an admin upload endpoint for
// publishing new bundles.
//
// Layout on disk (under OTA_STORE_DIR, default /opt/unipool/ota):
//
//	<runtime-version>/
//	    <update-id>/
//	        metadata.json        // expo export's metadata.json verbatim
//	        ios/                 // platform-specific assets + bundle
//	            <hash>.js        // launchAsset, identified by metadata
//	            <hash>.png
//	            ...
//	        android/
//	            ...
//
// `current` is whichever <update-id> sort-order-last for the
// runtime-version. We don't ship a separate pointer file because
// publish writes the directory atomically (tar extracted then renamed
// into place).
//
// Protocol spec: https://docs.expo.dev/technical-specs/expo-updates-1/
package ota

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// storeDir returns the root directory holding all published bundles.
// Read lazily on every call rather than at package init so godotenv
// has already populated the env by the time handlers fire (package
// init runs before main()'s .env load).
func storeDir() string {
	if v := strings.TrimSpace(os.Getenv("OTA_STORE_DIR")); v != "" {
		return v
	}
	return "/opt/unipool/ota"
}

// adminToken returns the shared secret accepted by /api/ota/upload.
// Empty disables the upload endpoint (the GET endpoints stay up so
// existing clients keep receiving their last-published bundle).
func adminToken() string {
	return strings.TrimSpace(os.Getenv("OTA_ADMIN_TOKEN"))
}

// publicBaseURL returns the URL prefix the client uses for asset GETs.
// The manifest embeds absolute asset URLs so the launchAsset and
// every PNG/font resolve cleanly.
func publicBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("OTA_PUBLIC_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://unidev.acmvit.in"
}

func safeOTASegment(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" &&
		value != "." &&
		value != ".." &&
		!filepath.IsAbs(value) &&
		!strings.ContainsAny(value, `/\`)
}

// expoExportMetadata is the on-disk shape produced by `npx expo
// export`. We only read the fields we need to construct manifests.
type expoExportMetadata struct {
	Version      int    `json:"version"`
	Bundler      string `json:"bundler"`
	FileMetadata struct {
		IOS     platformFiles `json:"ios"`
		Android platformFiles `json:"android"`
	} `json:"fileMetadata"`
}

type platformFiles struct {
	Bundle string          `json:"bundle"`
	Assets []platformAsset `json:"assets"`
}

type platformAsset struct {
	Path string `json:"path"`
	Ext  string `json:"ext"`
}

// manifestAsset is a single entry in the v1 manifest's assets array
// (and the shape of launchAsset).
type manifestAsset struct {
	Hash          string `json:"hash"`
	Key           string `json:"key"`
	ContentType   string `json:"contentType"`
	FileExtension string `json:"fileExtension,omitempty"`
	URL           string `json:"url"`
}

type manifestBody struct {
	ID             string          `json:"id"`
	CreatedAt      string          `json:"createdAt"`
	RuntimeVersion string          `json:"runtimeVersion"`
	LaunchAsset    manifestAsset   `json:"launchAsset"`
	Assets         []manifestAsset `json:"assets"`
	Metadata       map[string]any  `json:"metadata"`
	Extra          map[string]any  `json:"extra"`
}

// ManifestHandler implements GET /api/manifest. Clients send the
// runtime version + platform via headers; we look up the latest
// published bundle for that pair and return it as a multipart/mixed
// response (per protocol v1).
//
// Routes that should never be auth-gated — this fires before login.
func ManifestHandler(c *fiber.Ctx) error {
	platform := strings.ToLower(strings.TrimSpace(c.Get("expo-platform")))
	runtimeVersion := strings.TrimSpace(c.Get("expo-runtime-version"))
	protocol := strings.TrimSpace(c.Get("expo-protocol-version"))

	if platform != "ios" && platform != "android" {
		return c.Status(fiber.StatusBadRequest).
			SendString("unsupported expo-platform: " + platform)
	}
	if runtimeVersion == "" {
		return c.Status(fiber.StatusBadRequest).
			SendString("expo-runtime-version header is required")
	}
	if !safeOTASegment(runtimeVersion) {
		return c.Status(fiber.StatusBadRequest).
			SendString("invalid expo-runtime-version header")
	}
	if protocol != "" && protocol != "0" && protocol != "1" {
		return c.Status(fiber.StatusBadRequest).
			SendString("unsupported expo-protocol-version: " + protocol)
	}

	updateID, updateDir, err := latestUpdateDir(runtimeVersion)
	if err != nil {
		return c.Status(fiber.StatusNotFound).
			SendString("no published update for runtime " + runtimeVersion)
	}

	metadataPath := filepath.Join(updateDir, "metadata.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		log.Printf("ota: read metadata %s: %v", metadataPath, err)
		return c.Status(fiber.StatusInternalServerError).
			SendString("manifest metadata unreadable")
	}
	var meta expoExportMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		log.Printf("ota: parse metadata %s: %v", metadataPath, err)
		return c.Status(fiber.StatusInternalServerError).
			SendString("manifest metadata malformed")
	}

	pf := meta.FileMetadata.IOS
	if platform == "android" {
		pf = meta.FileMetadata.Android
	}
	if pf.Bundle == "" {
		return c.Status(fiber.StatusNotFound).
			SendString("no bundle for platform " + platform + " in update " + updateID)
	}

	createdAt, err := readCreatedAt(updateDir)
	if err != nil {
		log.Printf("ota: stat update dir %s: %v", updateDir, err)
		return c.Status(fiber.StatusInternalServerError).
			SendString("manifest createdAt unreadable")
	}

	launch, err := buildAsset(updateDir, pf.Bundle, "application/javascript", "", runtimeVersion, platform, updateID)
	if err != nil {
		log.Printf("ota: hash launch asset: %v", err)
		return c.Status(fiber.StatusInternalServerError).
			SendString("launch asset unreadable")
	}

	assets := make([]manifestAsset, 0, len(pf.Assets))
	for _, a := range pf.Assets {
		contentType := contentTypeForExt(a.Ext)
		entry, err := buildAsset(updateDir, a.Path, contentType, a.Ext, runtimeVersion, platform, updateID)
		if err != nil {
			log.Printf("ota: hash asset %s: %v", a.Path, err)
			return c.Status(fiber.StatusInternalServerError).
				SendString("asset unreadable: " + a.Path)
		}
		assets = append(assets, entry)
	}

	body := manifestBody{
		ID: updateID,
		// Millisecond precision is REQUIRED. The expo-updates native
		// clients parse createdAt with formatters that accept exactly
		// three fractional-second digits: iOS RCTConvert.nsDate uses
		// "yyyy-MM-dd'T'HH:mm:ss.SSSZZZZZ" and Android parseDateString
		// uses "...ss.SSS'Z'". Go's time.RFC3339Nano emits up to nine
		// digits (e.g. .446041554Z); that fails to parse, the update's
		// commitTime ends up nil/wrong, the update never finalizes, and
		// the client re-downloads the same bundle on every launch
		// without ever applying it. Format with .000 to force exactly
		// three digits.
		CreatedAt:      createdAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		RuntimeVersion: runtimeVersion,
		LaunchAsset:    launch,
		Assets:         assets,
		Metadata:       map[string]any{},
		Extra:          map[string]any{},
	}
	manifestJSON, err := json.Marshal(body)
	if err != nil {
		log.Printf("ota: marshal manifest: %v", err)
		return c.Status(fiber.StatusInternalServerError).
			SendString("manifest encode failed")
	}

	// Build a multipart/mixed response. Protocol v1 expects exactly
	// one part with name="manifest" and content-type application/json.
	// We let Fiber set the Content-Type via the boundary it produces
	// from the multipart.Writer.
	buf := &strings.Builder{}
	mw := multipart.NewWriter(stringWriter{buf})
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="manifest"`)
	partHeader.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(partHeader)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("multipart create")
	}
	if _, err := part.Write(manifestJSON); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("multipart write")
	}
	if err := mw.Close(); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("multipart close")
	}

	c.Set("expo-protocol-version", "1")
	c.Set("expo-sfv-version", "0")
	c.Set("cache-control", "no-store, no-cache, max-age=0, must-revalidate")
	c.Set("pragma", "no-cache")
	c.Set("expires", "0")
	c.Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	return c.SendString(buf.String())
}

// stringWriter adapts a *strings.Builder to io.Writer so the
// multipart writer can target it.
type stringWriter struct{ b *strings.Builder }

func (w stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// AssetHandler implements GET /api/assets. The query param `asset` is
// the on-disk path embedded in the manifest (relative to the
// update-dir root). `runtimeVersion` and `platform` come along for
// the ride so we don't have to figure out which update owns the asset
// from the path alone.
func AssetHandler(c *fiber.Ctx) error {
	assetPath := c.Query("asset")
	runtimeVersion := c.Query("runtimeVersion")
	platform := strings.ToLower(c.Query("platform"))
	updateID := strings.TrimSpace(c.Query("updateId"))
	if assetPath == "" || runtimeVersion == "" {
		return c.Status(fiber.StatusBadRequest).SendString("missing asset/runtimeVersion query params")
	}
	if !safeOTASegment(runtimeVersion) {
		return c.Status(fiber.StatusBadRequest).SendString("invalid runtimeVersion query param")
	}
	if platform != "" && platform != "ios" && platform != "android" {
		return c.Status(fiber.StatusBadRequest).SendString("invalid platform query param")
	}
	if updateID != "" && !safeOTASegment(updateID) {
		return c.Status(fiber.StatusBadRequest).SendString("invalid updateId query param")
	}

	updateDir, immutableAsset, err := resolveAssetUpdateDir(runtimeVersion, updateID, assetPath)
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("asset not found")
	}

	// Reject path traversal explicitly — assetPath should be relative
	// to updateDir and stay inside it.
	full := filepath.Join(updateDir, assetPath)
	clean, err := filepath.Abs(full)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid asset path")
	}
	absRoot, err := filepath.Abs(updateDir)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("root resolve")
	}
	if !strings.HasPrefix(clean, absRoot+string(filepath.Separator)) {
		return c.Status(fiber.StatusBadRequest).SendString("asset path escapes update dir")
	}

	f, err := os.Open(clean)
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("asset not found")
	}
	defer f.Close()

	if immutableAsset {
		// Manifest URLs include updateId, so each asset URL is immutable and
		// safe to cache aggressively. This prevents stale bundle/cache clashes
		// after a newer OTA is published for the same runtime.
		c.Set("cache-control", "public, max-age=31536000, immutable")
	} else {
		// Legacy manifests published before updateId was added can still fetch
		// assets, but those URLs are runtime-scoped and must not be cached long.
		c.Set("cache-control", "no-cache, max-age=0, must-revalidate")
	}
	stat, _ := f.Stat()
	if stat != nil {
		c.Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	}
	c.Set("Content-Type", contentTypeFromPath(assetPath))
	if _, err := io.Copy(c.Response().BodyWriter(), f); err != nil {
		log.Printf("ota: stream asset %s: %v", clean, err)
		return err
	}
	return nil
}

func resolveAssetUpdateDir(runtimeVersion, updateID, assetPath string) (string, bool, error) {
	if !safeOTASegment(runtimeVersion) || (updateID != "" && !safeOTASegment(updateID)) {
		return "", false, os.ErrInvalid
	}
	if updateID != "" {
		dir := filepath.Join(storeDir(), runtimeVersion, updateID)
		if _, err := os.Stat(filepath.Join(dir, assetPath)); err != nil {
			return "", false, err
		}
		return dir, true, nil
	}

	dirs, err := updateDirsNewestFirst(runtimeVersion)
	if err != nil {
		return "", false, err
	}
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir.path, assetPath)); err == nil {
			return dir.path, false, nil
		}
	}
	return "", false, os.ErrNotExist
}

// UploadHandler implements POST /api/ota/upload. Body is a tar.gz
// containing the output of `npx expo export` (the `dist/` directory).
// Auth: Bearer token matching OTA_ADMIN_TOKEN.
//
// Query params (required):
//
//	runtime_version  — the runtimeVersion this bundle targets
//
// Optional:
//
//	message          — free-text changelog stored as message.txt for
//	                   audit. Doesn't affect what the client sees.
func UploadHandler(c *fiber.Ctx) error {
	if adminToken() == "" {
		return c.Status(fiber.StatusServiceUnavailable).
			SendString("OTA upload disabled (set OTA_ADMIN_TOKEN to enable)")
	}
	got := strings.TrimSpace(strings.TrimPrefix(c.Get("Authorization"), "Bearer "))
	if got == "" || got != adminToken() {
		return c.Status(fiber.StatusUnauthorized).SendString("invalid admin token")
	}

	runtimeVersion := strings.TrimSpace(c.Query("runtime_version"))
	if runtimeVersion == "" {
		return c.Status(fiber.StatusBadRequest).SendString("runtime_version query param is required")
	}
	if !safeOTASegment(runtimeVersion) {
		return c.Status(fiber.StatusBadRequest).SendString("invalid runtime_version")
	}

	message := strings.TrimSpace(c.Query("message"))

	updateID := uuid.New().String()
	target := filepath.Join(storeDir(), runtimeVersion, updateID)
	staging := target + ".incoming"
	if err := os.MkdirAll(staging, 0o755); err != nil {
		log.Printf("ota: mkdir staging %s: %v", staging, err)
		return c.Status(fiber.StatusInternalServerError).SendString("staging mkdir failed")
	}

	// Extract the uploaded tar.gz into staging. Reject anything that
	// tries to write outside the staging root.
	body := c.Body()
	if len(body) == 0 {
		_ = os.RemoveAll(staging)
		return c.Status(fiber.StatusBadRequest).SendString("empty body — expected tar.gz")
	}
	gz, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		_ = os.RemoveAll(staging)
		return c.Status(fiber.StatusBadRequest).SendString("body is not gzip: " + err.Error())
	}
	defer gz.Close()
	if err := extractTar(gz, staging); err != nil {
		_ = os.RemoveAll(staging)
		log.Printf("ota: extract: %v", err)
		return c.Status(fiber.StatusBadRequest).SendString("tar extract failed: " + err.Error())
	}

	// `npx expo export` writes its bundle + assets under dist/. If
	// the uploader tarred dist/ itself (rather than its contents),
	// hoist them up so metadata.json sits at the root of the
	// update-dir.
	if _, err := os.Stat(filepath.Join(staging, "metadata.json")); err != nil {
		if inner, hoistErr := os.Stat(filepath.Join(staging, "dist", "metadata.json")); hoistErr == nil && !inner.IsDir() {
			tmpDist := filepath.Join(staging, "dist")
			entries, _ := os.ReadDir(tmpDist)
			for _, e := range entries {
				_ = os.Rename(filepath.Join(tmpDist, e.Name()), filepath.Join(staging, e.Name()))
			}
			_ = os.Remove(tmpDist)
		} else {
			_ = os.RemoveAll(staging)
			return c.Status(fiber.StatusBadRequest).
				SendString("uploaded archive missing metadata.json at root")
		}
	}

	if message != "" {
		_ = os.WriteFile(filepath.Join(staging, "message.txt"), []byte(message), 0o644)
	}

	// Atomic flip: manifest requests keep seeing the current update until
	// the staging directory is renamed into place.
	if err := os.Rename(staging, target); err != nil {
		_ = os.RemoveAll(staging)
		log.Printf("ota: finalize rename %s -> %s: %v", staging, target, err)
		return c.Status(fiber.StatusInternalServerError).SendString("finalize rename failed")
	}
	log.Printf("ota: published update %s for runtime %s", updateID, runtimeVersion)
	return c.JSON(fiber.Map{
		"update_id":       updateID,
		"runtime_version": runtimeVersion,
		"stored_at":       target,
	})
}

// latestUpdateDir picks the update with the lexicographically largest
// id under StoreDir/<runtime>/. Since update-ids are UUIDv4 they're
// effectively random, so the "latest" semantic comes from mtime
// rather than name sort.
func latestUpdateDir(runtime string) (string, string, error) {
	cands, err := updateDirsNewestFirst(runtime)
	if err != nil {
		return "", "", err
	}
	if len(cands) == 0 {
		return "", "", os.ErrNotExist
	}
	winner := cands[0]
	return winner.name, winner.path, nil
}

type updateDirCandidate struct {
	name string
	path string
	mod  time.Time
}

func updateDirsNewestFirst(runtime string) ([]updateDirCandidate, error) {
	if !safeOTASegment(runtime) {
		return nil, os.ErrInvalid
	}
	root := filepath.Join(storeDir(), runtime)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	cands := make([]updateDirCandidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".incoming") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		cands = append(cands, updateDirCandidate{
			name: name,
			path: filepath.Join(root, name),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return cands, nil
}

// readCreatedAt returns the directory's mtime as the update's
// canonical creation time. This survives backup/restore (modtime is
// preserved) and avoids stuffing a separate timestamp file into the
// layout.
func readCreatedAt(dir string) (time.Time, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}

// buildAsset hashes the file on disk and constructs the asset entry
// (including the public URL the client should fetch).
func buildAsset(updateDir, relPath, contentType, ext, runtimeVersion, platform, updateID string) (manifestAsset, error) {
	full := filepath.Join(updateDir, relPath)
	f, err := os.Open(full)
	if err != nil {
		return manifestAsset{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return manifestAsset{}, err
	}
	sum := h.Sum(nil)
	hash := base64.RawURLEncoding.EncodeToString(sum)
	// `key` per the protocol is the md5/sha hash used by the asset
	// system to cache. We reuse the same sha256 hash; the client
	// just treats it as opaque.
	url := fmt.Sprintf("%s/api/assets?asset=%s&runtimeVersion=%s&platform=%s&updateId=%s",
		publicBaseURL(),
		queryEscape(relPath),
		queryEscape(runtimeVersion),
		queryEscape(platform),
		queryEscape(updateID),
	)
	asset := manifestAsset{
		Hash:        hash,
		Key:         hash,
		ContentType: contentType,
		URL:         url,
	}
	if ext != "" {
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		asset.FileExtension = ext
	}
	_ = updateID
	return asset, nil
}

func queryEscape(s string) string {
	// stdlib url.QueryEscape would over-escape characters Expo accepts
	// in asset paths. We only need to escape the few that are
	// problematic in URL queries.
	r := strings.NewReplacer(
		"%", "%25",
		"&", "%26",
		"#", "%23",
		"+", "%2B",
		" ", "%20",
	)
	return r.Replace(s)
}

func contentTypeForExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "ttf", "otf":
		return "font/ttf"
	case "json":
		return "application/json"
	case "js", "bundle":
		return "application/javascript"
	case "mp3":
		return "audio/mpeg"
	case "mp4":
		return "video/mp4"
	case "wav":
		return "audio/wav"
	case "lottie":
		return "application/json"
	}
	return "application/octet-stream"
}

func contentTypeFromPath(p string) string {
	return contentTypeForExt(filepath.Ext(p))
}

// extractTar streams a tar archive into root, rejecting any entry
// whose target escapes root.
func extractTar(r io.Reader, root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, hdr.Name)
		clean, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(clean, absRoot+string(filepath.Separator)) && clean != absRoot {
			return fmt.Errorf("tar entry escapes root: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(clean, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(clean, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices, etc. — skip silently.
		}
	}
}
