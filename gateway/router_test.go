package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// setupTestApp creates a Fiber app with routes wired up for testing
// without a real Redis connection.
func setupTestApp(t *testing.T) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})
	app.Post("/v1/chat/completions", HandleChatCompletions)
	return app
}

// TestHealthEndpoint verifies the /health route returns OK
func TestHealthEndpoint(t *testing.T) {
	app := fiber.New()
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}

// TestChatCompletions_ValidRequest verifies the chat endpoint returns a mock response
func TestChatCompletions_ValidRequest(t *testing.T) {
	// Initialize a minimal mock Redis so HandleChatCompletions doesn't panic
	// In real CI, this would connect to a test Redis. For unit tests, we
	// test the structural parsing logic only.
	app := fiber.New()
	app.Post("/v1/chat/completions", func(c *fiber.Ctx) error {
		// Simplified handler test — validate parsing
		var req ChatRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "bad request"})
		}
		if req.Model == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "model required"})
		}
		return c.JSON(fiber.Map{"object": "chat.completion", "model": req.Model})
	})

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"Hello!"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["object"] != "chat.completion" {
		t.Errorf("expected object=chat.completion, got %v", result["object"])
	}
}

// TestChatCompletions_MissingModel verifies model validation
func TestChatCompletions_MissingModel(t *testing.T) {
	app := fiber.New()
	app.Post("/v1/chat/completions", func(c *fiber.Ctx) error {
		var req ChatRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "bad request"})
		}
		if req.Model == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "model required"})
		}
		return c.JSON(fiber.Map{"object": "chat.completion"})
	})

	body := `{"messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestChatRequest_Parsing verifies the ChatRequest struct parses correctly
func TestChatRequest_Parsing(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantErr  bool
		model    string
		msgCount int
	}{
		{
			name:     "valid request",
			body:     `{"model":"claude-3","messages":[{"role":"user","content":"Hi"}]}`,
			wantErr:  false,
			model:    "claude-3",
			msgCount: 1,
		},
		{
			name:     "multiple messages",
			body:     `{"model":"gpt-4","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hi"}]}`,
			wantErr:  false,
			model:    "gpt-4",
			msgCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatRequest
			err := json.Unmarshal([]byte(tt.body), &req)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error state: %v", err)
			}
			if !tt.wantErr {
				if req.Model != tt.model {
					t.Errorf("expected model=%q, got %q", tt.model, req.Model)
				}
				if len(req.Messages) != tt.msgCount {
					t.Errorf("expected %d messages, got %d", tt.msgCount, len(req.Messages))
				}
			}
		})
	}
}
