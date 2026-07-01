package helpers

import "strings"

const (
	CanonicalVITVelloreLat = 12.969193
	CanonicalVITVelloreLon = 79.155968
)

var canonicalLocationCoords = map[string][2]float64{
	"vit vellore":                             {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
	"vit vellore india":                       {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
	"vit vellore main gate":                   {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
	"vellore institute of technology":         {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
	"vellore institute of technology vellore": {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
	"vellore institute of technology india":   {CanonicalVITVelloreLat, CanonicalVITVelloreLon},
}

// CanonicalLocationCoords returns product-owned coordinates for exact
// high-traffic labels. It intentionally avoids generic aliases like "vit" or
// "vellore" so we do not rewrite VIT Chennai or a city-level Vellore search.
func CanonicalLocationCoords(label string) (float64, float64, bool) {
	coords, ok := canonicalLocationCoords[normalizeCanonicalLocationLabel(label)]
	if !ok {
		return 0, 0, false
	}
	return coords[0], coords[1], true
}

func normalizeCanonicalLocationLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
