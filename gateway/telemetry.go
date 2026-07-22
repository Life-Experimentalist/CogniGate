package main

import (
	"log"
)

// TelemetryPayload represents usage metrics for a specific tenant.
type TelemetryPayload struct {
	TenantID         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// DispatchTelemetry sends usage metrics asynchronously to the Java Domain Engine.
func DispatchTelemetry(tenantID string, prompt, completion, total int) {
	payload := TelemetryPayload{
		TenantID:         tenantID,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
	}

	// Fire-and-forget goroutine
	go func(metrics TelemetryPayload) {
		log.Printf("[Telemetry] Dispatching usage for tenant %s: %d total tokens", metrics.TenantID, metrics.TotalTokens)
		// TODO: Send HTTP POST or push to Redis Stream for the Java backend to process
	}(payload)
}
