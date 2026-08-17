package com.cognigate.dto;

import com.fasterxml.jackson.annotation.JsonProperty;

import java.math.BigDecimal;
import java.time.Instant;

/**
 * One metered request, as the gateway sends it.
 *
 * <p>The field names are the gateway's own wire names, so the two sides of the
 * contract can be read against each other without a translation table. It
 * carries no prompt or completion content: GW-14 forbids that in any durable
 * store, and this is the durable store.
 *
 * <p>{@code recordedAt} is the gateway's timestamp, not the moment this arrives.
 * A record replayed after an analytics outage belongs to the window in which the
 * request was actually served, and stamping it on receipt would move a week of
 * buffered usage into whichever day the service came back up.
 */
public record UsageRecordRequest(
        @JsonProperty("request_id") String requestId,
        @JsonProperty("client_request_id") String clientRequestId,
        @JsonProperty("tenant_id") String tenantId,
        @JsonProperty("key_prefix") String keyPrefix,
        @JsonProperty("provider") String provider,
        @JsonProperty("model") String model,
        @JsonProperty("requested_model") String requestedModel,
        @JsonProperty("fallback_depth") int fallbackDepth,
        @JsonProperty("prompt_tokens") int promptTokens,
        @JsonProperty("completion_tokens") int completionTokens,
        @JsonProperty("total_tokens") int totalTokens,
        @JsonProperty("cost_usd") BigDecimal costUsd,
        @JsonProperty("cached") boolean cached,
        @JsonProperty("streamed") boolean streamed,
        @JsonProperty("status_code") int statusCode,
        @JsonProperty("duration_ms") long durationMs,
        @JsonProperty("recorded_at") Instant recordedAt) {
}
