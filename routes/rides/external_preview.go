package rides

import "github.com/gofiber/fiber/v2"

// GetExternalRidePreview returns a single external ride by id for the web
// ride-detail page (/ride/<id>). Public so shared links open signed-out; it
// serves the same sanitized ExternalRideCard the nearby/search endpoints do,
// and the host email stays hidden (ExternalRideCard.HostEmail is json:"-").
func GetExternalRidePreview(c *fiber.Ctx) error {
	id := c.Params("id")
	ride, ok := FindExternalRideByID(id)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not_found"})
	}
	return c.JSON(ride)
}
