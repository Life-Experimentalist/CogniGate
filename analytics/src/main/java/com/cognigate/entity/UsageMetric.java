package com.cognigate.entity;

import jakarta.persistence.*;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;

import java.math.BigDecimal;
import java.time.Instant;

/**
 * One metered request, durably stored.
 *
 * <p>The tenant is a plain string rather than a foreign key to {@link Tenant}.
 * The gateway is the authority on tenant identity — it mints them, and it
 * authenticates against its own store — so a foreign key here would mean usage
 * could not be recorded until tenant rows had been synchronised into this
 * service first. That would make a metering write depend on a CRUD path it has
 * no reason to touch, and would drop records for exactly the tenant that was
 * created most recently.
 *
 * <p>{@code requestId} is unique. The gateway retries a delivery whose response
 * it never saw, so the same record can legitimately arrive twice; the
 * constraint is what makes the second arrival a no-op instead of double
 * billing.
 *
 * <p>No prompt or completion text is stored, only the dimensions billing and
 * debugging need (GW-14).
 */
@Entity
@Table(
        name = "usage_metric",
        uniqueConstraints = @UniqueConstraint(
                name = "uk_usage_metric_request_id", columnNames = "request_id"),
        indexes = {
                // Every read is "one tenant, one window", optionally narrowed to
                // one key. These two cover both, and leading with the tenant is
                // what keeps one tenant's volume out of another's query plan.
                @Index(name = "ix_usage_metric_tenant_recorded",
                        columnList = "tenant_id,recorded_at"),
                @Index(name = "ix_usage_metric_key_recorded",
                        columnList = "tenant_id,key_prefix,recorded_at")
        })
@Data
@NoArgsConstructor
@AllArgsConstructor
public class UsageMetric {

    @Id
    @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "request_id", nullable = false, updatable = false, length = 128)
    private String requestId;

    /** The caller's own correlation id, when it supplied one. */
    @Column(name = "client_request_id", length = 128)
    private String clientRequestId;

    @Column(name = "tenant_id", nullable = false, length = 128)
    private String tenantId;

    /** The non-secret leading characters of the key that authenticated the request. */
    @Column(name = "key_prefix", length = 64)
    private String keyPrefix;

    @Column(name = "provider", length = 64)
    private String provider;

    /** The model actually served, which a fallback may make different from the one asked for. */
    @Column(name = "model", length = 256)
    private String model;

    @Column(name = "requested_model", length = 256)
    private String requestedModel;

    @Column(name = "fallback_depth", nullable = false)
    private Integer fallbackDepth;

    @Column(name = "prompt_tokens", nullable = false)
    private Integer promptTokens;

    @Column(name = "completion_tokens", nullable = false)
    private Integer completionTokens;

    @Column(name = "total_tokens", nullable = false)
    private Integer totalTokens;

    /**
     * What the request cost, as the gateway priced it.
     *
     * <p>Scale 8 because a single small completion can cost a few millionths of
     * a dollar, and rounding those to cents at write time would make a month of
     * them add up to zero.
     */
    @Column(name = "cost_usd", nullable = false, precision = 19, scale = 8)
    private BigDecimal costUsd;

    @Column(name = "cached", nullable = false)
    private Boolean cached;

    @Column(name = "streamed", nullable = false)
    private Boolean streamed;

    @Column(name = "status_code", nullable = false)
    private Integer statusCode;

    @Column(name = "duration_ms", nullable = false)
    private Long durationMs;

    /** When the gateway served the request, never when this service received it. */
    @Column(name = "recorded_at", nullable = false)
    private Instant recordedAt;
}
