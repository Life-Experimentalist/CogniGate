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
        @JsonProperty("prompt_tokens") Long promptTokens,
        @JsonProperty("completion_tokens") Long completionTokens,
        @JsonProperty("total_tokens") Long totalTokens,
        @JsonProperty("cost_usd") BigDecimal costUsd) {

    public UsageBucketResponse {
        requests = requests == null ? 0L : requests;
        promptTokens = promptTokens == null ? 0L : promptTokens;
        completionTokens = completionTokens == null ? 0L : completionTokens;
        totalTokens = totalTokens == null ? 0L : totalTokens;
        costUsd = costUsd == null ? BigDecimal.ZERO : costUsd;
    }
}
