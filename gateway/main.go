package main

import (
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	// 1. Initialize Redis connection
	InitRedis()

	// 2. Initialize Fiber app
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	// 3. Attach Middlewares
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${method} ${path}\n",
	}))

	// 4. Register Routes
	app.Post("/v1/chat/completions", HandleChatCompletions)

	// Health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// 5. Start Server
	log.Println("CogniGate Edge Proxy starting on port 8080...")
	if err := app.Listen(":8080"); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}
