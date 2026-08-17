package com.cognigate.controller;

import com.cognigate.dto.UsageBucketResponse;
import com.cognigate.dto.UsageRecordRequest;
import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.entity.UsageMetric;
import com.cognigate.repository.UsageMetricRepo;
import org.springframework.dao.DataIntegrityViolationException;
import org.springframework.format.annotation.DateTimeFormat;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;
import java.util.Map;

/**
 * The usage plane: where the gateway's metering is stored and read back.
 *
 * <p>This is the durable half of a CogniGate deployment. The gateway keeps
 * tenants, keys and routing in its own process and serves them from there; it
 * sends usage here because usage is the one thing a restart must not lose
 * (GW-11).
 *
 * <p>The status codes are a contract, not decoration. The gateway retries a
 * failed delivery indefinitely so that an outage here costs no billing data,
 * which only works if it can tell "try again later" from "this will never
 * work": a 5xx is retried, a 4xx is dropped. Answering 500 to a malformed
 * record would wedge the gateway's queue behind it forever.
 */
@RestController
@RequestMapping("/api/v1/usage")
public class UsageController {

    private final UsageMetricRepo usageMetricRepo;

    public UsageController(UsageMetricRepo usageMetricRepo) {
        this.usageMetricRepo = usageMetricRepo;
    }

    /**
     * Stores one metered request.
     *
     * <p>Idempotent on {@code request_id}: 201 when the record is new, 200 when
     * it was already held. The gateway treats both as delivered, which is what
     * lets it safely retry a write whose response was lost.
     */
    @PostMapping
    public ResponseEntity<Object> record(@RequestBody UsageRecordRequest request) {
        if (isBlank(request.requestId())) {
            return badRequest("request_id is required.");
        }
        if (isBlank(request.tenantId())) {
            return badRequest("tenant_id is required.");
        }
        // Rejected rather than defaulted to now: a record replayed after an
        // outage would land in the wrong billing period, and a wrong invoice is
        // worse than a refused write the sender can see and fix.
        if (request.recordedAt() == null) {
            return badRequest("recorded_at is required.");
        }

        if (usageMetricRepo.existsByRequestId(request.requestId())) {
            return ResponseEntity.ok().build();
        }
        try {
            usageMetricRepo.save(toEntity(request));
        } catch (DataIntegrityViolationException e) {
            // Two deliveries of the same record raced. Anything else that
            // violates a constraint is a real failure and must stay one.
            if (usageMetricRepo.existsByRequestId(request.requestId())) {
                return ResponseEntity.ok().build();
            }
            throw e;
        }
        return ResponseEntity.status(HttpStatus.CREATED).build();
    }

    /**
     * Totals over a half-open window, for the whole tenant or for one key.
     */
    @GetMapping("/totals")
    public ResponseEntity<Object> totals(
            @RequestParam("tenant_id") String tenantId,
            @RequestParam(name = "key_prefix", required = false) String keyPrefix,
            @RequestParam("since") @DateTimeFormat(iso = DateTimeFormat.ISO.DATE_TIME) Instant since,
            @RequestParam("until") @DateTimeFormat(iso = DateTimeFormat.ISO.DATE_TIME) Instant until) {

        UsageTotalsResponse totals = isBlank(keyPrefix)
                ? usageMetricRepo.totals(tenantId, since, until)
                : usageMetricRepo.keyTotals(tenantId, keyPrefix, since, until);
        return ResponseEntity.ok(totals);
    }

    /**
     * The same window grouped by one dimension, most expensive row first.
     */
    @GetMapping("/breakdown")
    public ResponseEntity<Object> breakdown(
            @RequestParam("tenant_id") String tenantId,
            @RequestParam("group_by") String groupBy,
            @RequestParam("since") @DateTimeFormat(iso = DateTimeFormat.ISO.DATE_TIME) Instant since,
            @RequestParam("until") @DateTimeFormat(iso = DateTimeFormat.ISO.DATE_TIME) Instant until) {

        List<UsageBucketResponse> rows;
        switch (groupBy) {
            case "model" -> rows = usageMetricRepo.breakdownByModel(tenantId, since, until);
            case "provider" -> rows = usageMetricRepo.breakdownByProvider(tenantId, since, until);
            case "key" -> rows = usageMetricRepo.breakdownByKey(tenantId, since, until);
            case "client_request_id" ->
                    rows = usageMetricRepo.breakdownByClientRequestId(tenantId, since, until);
            default -> {
                return badRequest(
                        "group_by must be one of model, provider, key, client_request_id.");
            }
        }
        return ResponseEntity.ok(rows);
    }

    private static UsageMetric toEntity(UsageRecordRequest r) {
        UsageMetric metric = new UsageMetric();
        metric.setRequestId(r.requestId());
        metric.setClientRequestId(r.clientRequestId());
        metric.setTenantId(r.tenantId());
        metric.setKeyPrefix(r.keyPrefix());
        metric.setProvider(r.provider());
        metric.setModel(r.model());
        metric.setRequestedModel(r.requestedModel());
        metric.setFallbackDepth(r.fallbackDepth());
        metric.setPromptTokens(r.promptTokens());
        metric.setCompletionTokens(r.completionTokens());
        metric.setTotalTokens(r.totalTokens());
        metric.setCostUsd(r.costUsd() == null ? BigDecimal.ZERO : r.costUsd());
        metric.setCached(r.cached());
        metric.setStreamed(r.streamed());
        metric.setStatusCode(r.statusCode());
        metric.setDurationMs(r.durationMs());
        metric.setRecordedAt(r.recordedAt());
        return metric;
    }

    private static boolean isBlank(String s) {
        return s == null || s.isBlank();
    }

    /**
     * A refusal the sender must not retry. The body is JSON because the caller
     * asks for JSON, and a plain-text error would be answered with a 406 it
     * could not read.
     */
    private static ResponseEntity<Object> badRequest(String message) {
        return ResponseEntity.badRequest().body(Map.of("error", message));
    }
}
