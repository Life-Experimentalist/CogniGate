package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/gofiber/fiber/v2"
)

// ChatMessage represents a single message in the chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents the standard OpenAI chat completion request.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

// ChatResponse represents the standard OpenAI chat completion response.
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// HandleChatCompletions handles POST /v1/chat/completions
func HandleChatCompletions(c *fiber.Ctx) error {
	var req ChatRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// 1. Extract Bearer Token
	authHeader := c.Get("Authorization")
	if authHeader == "" || len(authHeader) < 7 || authHeader[:7] != "Bearer " {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Unauthorized",
		})
	}
	apiKey := authHeader[7:]

	// 2. Validate Tenant from Redis (Mocking the validation for Phase 1)
	tenantConfig, err := Rdb.Get(ctx, "tenant:cfg:"+apiKey).Result()
	if err != nil {
		// For the sake of the test (so `curl` works without us actually seeding Redis),
		// we'll allow "test" as a master bypass.
		if apiKey != "test" {
			log.Printf("Failed to find config for key %s: %v", apiKey, err)
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid API key",
			})
		}
		tenantConfig = `{"tenant_id": "test-org", "plan": "enterprise"}`
	}

	// 3. Round-Robin Key Rotation & Circuit Breaker (Mocked)
	// In a full implementation, we'd pull keys, check for backoff in Redis, and iterate.
	// For now, we simulate success via the "primary" or cascading to "backup".
	log.Printf("Routing request for model: %s, tenant: %s", req.Model, tenantConfig)

	// Simulate external request delay
	time.Sleep(50 * time.Millisecond)

	// 4. Dispatch Telemetry asynchronously
	DispatchTelemetry("test-org", 15, 20, 35)

	// 5. Return Mock Response
	resp := ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", rand.Int()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
	}
	resp.Choices = append(resp.Choices, struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	}{
		Message: ChatMessage{
			Role:    "assistant",
			Content: "Hello from CogniGate! (Mock Response)",
		},
		FinishReason: "stop",
	})
	resp.Usage.PromptTokens = 15
	resp.Usage.CompletionTokens = 20
	resp.Usage.TotalTokens = 35

	return c.JSON(resp)
}
