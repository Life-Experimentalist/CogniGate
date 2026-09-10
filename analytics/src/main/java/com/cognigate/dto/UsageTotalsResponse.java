package com.cognigate.dto;

import com.fasterxml.jackson.annotation.JsonProperty;

import java.math.BigDecimal;

/**
 * The aggregate behind the gateway's {@code GET /v1/usage}.
 *
 * <p>Built directly by a JPQL constructor expression, so a window's totals come
 * back as a handful of numbers rather than as every row that produced them.
 *
 * <p>The compact constructor is what makes that safe: an aggregate query over a
 * window with no rows returns one row of nulls, and a tenant that has sent
 * nothing has used nothing, not an unknown amount.
 */
public record UsageTotalsResponse(
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

    public UsageTotalsResponse {
        requests = requests == null ? 0L : requests;
        cachedRequests = cachedRequests == null ? 0L : cachedRequests;
        promptTokens = promptTokens == null ? 0L : promptTokens;
        completionTokens = completionTokens == null ? 0L : completionTokens;
        totalTokens = totalTokens == null ? 0L : totalTokens;
        costUsd = costUsd == null ? BigDecimal.ZERO : costUsd;
        chargeUsd = chargeUsd == null ? BigDecimal.ZERO : chargeUsd;
    }
}
