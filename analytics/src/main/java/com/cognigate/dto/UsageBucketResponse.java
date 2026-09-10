package com.cognigate.dto;

import com.fasterxml.jackson.annotation.JsonProperty;

import java.math.BigDecimal;

/**
 * One row of the gateway's {@code GET /v1/usage/breakdown}.
 *
 * <p>Flat rather than a key beside a nested totals object, because that is the
 * shape the gateway publishes: its own type embeds the totals, so they appear
 * as siblings of the key.
 */
public record UsageBucketResponse(
        @JsonProperty("key") String key,
        @JsonProperty("requests") Long requests,
        // How many of the requests the gateway answered from its completion
        // cache. A cache hit consumes no tokens and costs nothing, so this is
        // why a window's spend can look low against its request count.
        @JsonProperty("cached_requests") Long cachedRequests,
        @JsonProperty("prompt_tokens") Long promptTokens,
        @JsonProperty("completion_tokens") Long completionTokens,
        @JsonProperty("total_tokens") Long totalTokens,
        @JsonProperty("cost_usd") BigDecimal costUsd,
        @JsonProperty("charge_usd") BigDecimal chargeUsd) {

    public UsageBucketResponse {
        requests = requests == null ? 0L : requests;
        cachedRequests = cachedRequests == null ? 0L : cachedRequests;
        promptTokens = promptTokens == null ? 0L : promptTokens;
        completionTokens = completionTokens == null ? 0L : completionTokens;
        totalTokens = totalTokens == null ? 0L : totalTokens;
        costUsd = costUsd == null ? BigDecimal.ZERO : costUsd;
        chargeUsd = chargeUsd == null ? BigDecimal.ZERO : chargeUsd;
    }
}
