package chat

import (
    "encoding/json"
    "log"
    "strconv"
    "time"

    "github.com/gofiber/fiber/v2"
    "github.com/gofiber/websocket/v2"
    "github.com/google/uuid"
    "unipool-backend/database"
    "unipool-backend/initializer"
    "unipool-backend/models"
    "unipool-backend/services"
)

func GetRideMessages(c *fiber.Ctx) error {
    rideID := c.Params("ride_id")
    rideUUID, err := uuid.Parse(rideID)
    if err != nil {
        return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
    }

    var messages []models.Message
    if err := database.Database.Db.Preload("Sender").Where("ride_id = ?", rideUUID).Order("created_at asc").Find(&messages).Error; err != nil {
        return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
    }

    return c.JSON(fiber.Map{"messages": messages, "count": len(messages)})
}

func SendMessage(c *fiber.Ctx) error {
	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	msg := models.Message{RideID: rideUUID, SenderID: user.ID, Content: body.Content}
	if err := database.Database.Db.Create(&msg).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save message"})
	}
	database.Database.Db.Preload("Sender").First(&msg, msg.ID)

	chatMsg := initializer.ChatMessage{
		Type:      "message",
		RoomID:    rideID,
		SenderID:  user.ID.String(),
		Content:   body.Content,
		Timestamp: msg.CreatedAt.Format(time.RFC3339),
		MessageID: msg.ID.String(),
	}
	if hub := initializer.GetChatHub(); hub != nil {
		if b, err := json.Marshal(chatMsg); err == nil {
			hub.BroadcastToRoom(rideID, b)
		}
	}

	// Send FCM notifications to other participants
	fcmService := services.GetFCMService()
	if fcmService != nil {
		go func() {
			// Get ride details
			var ride models.Ride
			if err := database.Database.Db.Preload("HostUser").First(&ride, rideUUID).Error; err != nil {
				log.Printf("Error fetching ride for notifications: %v", err)
				return
			}

			// Get all bookings for this ride
			var bookings []models.Booking
			if err := database.Database.Db.Preload("Passenger").Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").Find(&bookings).Error; err != nil {
				log.Printf("Error fetching bookings for notifications: %v", err)
				return
			}

			rideRoute := ride.StartLocation + " to " + ride.EndLocation
			
			// Notify ride host if the sender is not the host
			if ride.HostUserID != user.ID {
				if err := fcmService.SendChatMessageNotification(ride.HostUserID, user.Name, body.Content, rideRoute, rideUUID); err != nil {
					log.Printf("Error sending chat notification to host: %v", err)
				}
			}

			// Notify all passengers except the sender
			for _, booking := range bookings {
				if booking.PassengerID != user.ID {
					if err := fcmService.SendChatMessageNotification(booking.PassengerID, user.Name, body.Content, rideRoute, rideUUID); err != nil {
						log.Printf("Error sending chat notification to passenger %s: %v", booking.PassengerID, err)
					}
				}
			}
		}()
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"message": msg})
}

func GetUserChats(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

    var rides []models.Ride
    if err := database.Database.Db.Preload("HostUser").Where("host_user_id = ?", userUUID).
        Or("id IN (SELECT ride_id FROM bookings WHERE user_id = ?)", userUUID).
        Order("created_at desc").Find(&rides).Error; err != nil {
        return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch chats"})
    }

    var chatRooms []fiber.Map
    for _, ride := range rides {
        var last models.Message
        database.Database.Db.Preload("Sender").Where("ride_id = ?", ride.ID).Order("created_at desc").First(&last)

        room := fiber.Map{
            "id":           ride.ID.String(),
            "title":        ride.StartLocation + " to " + ride.EndLocation,
            "subtitle":     "Trip on " + ride.StartTime.Format("Mon 2 Jan 2006"),
            "participants": strconv.Itoa(int(ride.BookedSeats)) + " passengers",
        }
        if last.ID != uuid.Nil {
            room["last_message"] = fiber.Map{
                "content":   last.Content,
                "sender":    last.Sender.Name,
                "timestamp": last.CreatedAt,
            }
        }
        chatRooms = append(chatRooms, room)
    }

    return c.JSON(fiber.Map{"chat_rooms": chatRooms, "count": len(chatRooms)})
}

func WebSocketHandler(c *websocket.Conn) {
    log.Printf("WebSocketHandler invoked. Query: %s", c.Query(""))
    userID := c.Query("user_id")
    roomID := c.Query("room_id")

    if userID == "" || roomID == "" {
        log.Printf("WebSocket connection rejected: missing user_id or room_id")
        c.WriteMessage(websocket.CloseMessage, []byte("missing user_id or room_id"))
        c.Close()
        return
    }

    initializer.NewClient(c, userID, roomID)
    select {}
}
