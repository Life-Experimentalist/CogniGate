package com.cognigate.dto;

import com.fasterxml.jackson.annotation.JsonProperty;

import java.math.BigDecimal;

/**
 * The aggregate behind the gateway's {@code GET /v1/usage}.
 *
 * <p>Built directly by a JPQL constructor expression, so a window's totals come
 * back as five numbers rather than as every row that produced them.
 *
 * <p>The compact constructor is what makes that safe: an aggregate query over a
 * window with no rows returns one row of nulls, and a tenant that has sent
 * nothing has used nothing, not an unknown amount.
 */
public record UsageTotalsResponse(
        @JsonProperty("requests") Long requests,
        @JsonProperty("prompt_tokens") Long promptTokens,
        @JsonProperty("completion_tokens") Long completionTokens,
        @JsonProperty("total_tokens") Long totalTokens,
        @JsonProperty("cost_usd") BigDecimal costUsd) {

    public UsageTotalsResponse {
        requests = requests == null ? 0L : requests;
        promptTokens = promptTokens == null ? 0L : promptTokens;
        completionTokens = completionTokens == null ? 0L : completionTokens;
        totalTokens = totalTokens == null ? 0L : totalTokens;
        costUsd = costUsd == null ? BigDecimal.ZERO : costUsd;
    }
}
