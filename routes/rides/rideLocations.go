package rides

import (
	"log"

	"github.com/gofiber/fiber/v2"
)

func RideLocation(c *fiber.Ctx) error {

	var startLocations = []string{"VIT Vellore", "VIT Chennai", "Chennai Airport", "Bangalore Airport", "Chennai", "Bangalore", "Katpadi Station", "Chittoor Bus Stand", "Vellore Bus Stand", "Pondicherry"}
	var endLocations = []string{"VIT Vellore", "VIT Chennai", "Chennai Airport", "Bangalore Airport", "Chennai", "Bangalore", "Katpadi Station", "Chittoor Bus Stand", "Vellore Bus Stand", "Pondicherry"}
	if len(startLocations) == 0 || len(endLocations) == 0 {
		log.Printf("Ride Locations Empty\n")
		return c.Status(500).SendString("Error fetching ride locations")
	}

	return c.JSON(fiber.Map{
		"startLocations": startLocations,
		"endLocations":   endLocations,
	})

}
