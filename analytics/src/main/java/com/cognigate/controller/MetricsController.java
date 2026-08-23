package com.cognigate.controller;

import io.micrometer.prometheusmetrics.PrometheusMeterRegistry;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * The scrape endpoint GW-8 requires on both processes.
 *
 * <p>The gateway serves its own at {@code /metrics}; this is the analytics
 * half, which the specification asks for in the same words and the same format.
 * A deployment scraping only the gateway can say how much traffic was routed
 * but nothing about the service that stores it — whether ingestion is keeping
 * up, whether the JVM is close to its heap, whether {@code POST /api/v1/usage}
 * has started answering 5xx and quietly stalling the gateway's delivery queue.
 *
 * <p>It is a controller rather than actuator's Prometheus endpoint because the
 * specification names the path {@code /metrics}, and actuator serves its
 * endpoints under {@code /actuator}. Moving actuator's base path to the root to
 * reach the same URL would have dragged the health endpoint along with it,
 * breaking both the container's HEALTHCHECK and the rule in
 * {@link com.cognigate.config.SecurityConfig} that opens health and nothing
 * else. Rendering the registry here costs six lines and moves nothing.
 *
 * <p>The series themselves are Micrometer's own — JVM, and one timer per MVC
 * route. No counter is added on top: the ingestion rate an operator wants is
 * already {@code http_server_requests_seconds_count} for
 * {@code /api/v1/usage}, and a second series counting the same events would
 * only be a second thing to keep correct.
 */
@RestController
@ConditionalOnProperty(name = "metrics.enabled", havingValue = "true", matchIfMissing = true)
public class MetricsController {

    private final PrometheusMeterRegistry registry;

    public MetricsController(PrometheusMeterRegistry registry) {
        this.registry = registry;
    }

    /**
     * Unauthenticated, which GW-8 requires rather than merely permits: a
     * scrape must not have to present a {@code cg-} or {@code cga-} key,
     * because scrapers are not tenants.
     * Prometheus has no way to present the analytics token, and a deployment
     * that wants this closed is told to bind it elsewhere or firewall the port.
     *
     * <p>Nothing here is tenant data. The series are process-level and route-level
     * only — no key, no prompt, no per-request identifier — so what an
     * unauthenticated scrape discloses is that this is a Spring service and how
     * busy it is.
     */
    @GetMapping(value = "/metrics", produces = "text/plain;version=0.0.4;charset=utf-8")
    public ResponseEntity<String> scrape() {
        return ResponseEntity.ok(registry.scrape());
    }
}
