package com.cognigate.controller;

import com.cognigate.entity.Tenant;
import com.cognigate.entity.UsageMetric;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.UsageMetricRepo;
import lombok.Data;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.time.LocalDateTime;
import java.util.Optional;

@RestController
@RequestMapping("/api/webhook")
public class WebhookController {

    private final TenantRepository tenantRepository;
    private final UsageMetricRepo usageMetricRepo;

    public WebhookController(TenantRepository tenantRepository, UsageMetricRepo usageMetricRepo) {
        this.tenantRepository = tenantRepository;
        this.usageMetricRepo = usageMetricRepo;
    }

    @PostMapping("/telemetry")
    public ResponseEntity<?> receiveTelemetry(@RequestBody TelemetryRequest request) {
        Optional<Tenant> tenantOpt = tenantRepository.findByName(request.getTenantId());
        if (tenantOpt.isEmpty()) {
            return ResponseEntity.status(404).body("Tenant not found: " + request.getTenantId());
        }

        Tenant tenant = tenantOpt.get();
        UsageMetric metric = new UsageMetric();
        metric.setTenant(tenant);
        metric.setPromptTokens(request.getPromptTokens());
        metric.setCompletionTokens(request.getCompletionTokens());
        metric.setTotalTokens(request.getTotalTokens());
        metric.setRecordedAt(LocalDateTime.now());

        usageMetricRepo.save(metric);

        return ResponseEntity.ok("Telemetry recorded successfully");
    }

    @Data
    public static class TelemetryRequest {
        private String tenantId;
        private int promptTokens;
        private int completionTokens;
        private int totalTokens;
    }
}
