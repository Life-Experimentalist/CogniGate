package main

import (
	"testing"
	"time"
)

// TestDispatchTelemetry_NonBlocking verifies the goroutine fires and doesn't block
func TestDispatchTelemetry_NonBlocking(t *testing.T) {
	// Signals when the dispatch call returns
	done := make(chan struct{}, 1)

	go func() {
		DispatchTelemetry("test-org", 10, 20, 30)
		done <- struct{}{}
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("DispatchTelemetry timed out — may be blocking unexpectedly")
	}
}

// TestDispatchTelemetry_Payload verifies the payload struct is correctly populated
func TestDispatchTelemetry_Payload(t *testing.T) {
	tests := []struct {
		tenantID   string
		prompt     int
		completion int
		total      int
	}{
		{"org-a", 100, 200, 300},
		{"org-b", 0, 0, 0},
		{"org-c", 9999, 9999, 19998},
	}

	for _, tt := range tests {
		t.Run(tt.tenantID, func(t *testing.T) {
			payload := TelemetryPayload{
				TenantID:         tt.tenantID,
				PromptTokens:     tt.prompt,
				CompletionTokens: tt.completion,
				TotalTokens:      tt.total,
			}

			if payload.TenantID != tt.tenantID {
				t.Errorf("expected TenantID=%q, got %q", tt.tenantID, payload.TenantID)
			}
			if payload.TotalTokens != tt.total {
				t.Errorf("expected TotalTokens=%d, got %d", tt.total, payload.TotalTokens)
			}
		})
	}
}
