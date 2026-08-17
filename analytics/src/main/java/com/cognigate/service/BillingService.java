package com.cognigate.service;

import com.cognigate.entity.Tenant;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.UsageMetricRepo;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

import java.math.BigDecimal;
import java.time.Duration;
import java.time.Instant;
import java.util.List;

@Service
public class BillingService {

    private static final Logger log = LoggerFactory.getLogger(BillingService.class);

    private final TenantRepository tenantRepository;
    private final UsageMetricRepo usageMetricRepo;

    // Price per 1K tokens ($)
    private static final BigDecimal COST_PER_THOUSAND_TOKENS = new BigDecimal("0.0015");

    public BillingService(TenantRepository tenantRepository, UsageMetricRepo usageMetricRepo) {
        this.tenantRepository = tenantRepository;
        this.usageMetricRepo = usageMetricRepo;
    }

    /**
     * Run at midnight on the first day of every month to process billing.
     */
    @Scheduled(cron = "0 0 0 1 * ?")
    public void runMonthlyBilling() {
        log.info("Starting scheduled monthly billing processing...");
        List<Tenant> tenants = tenantRepository.findAll();
        Instant end = Instant.now();
        Instant start = end.minus(Duration.ofDays(30));

        for (Tenant tenant : tenants) {
            calculateTenantInvoice(tenant, start, end);
        }
        log.info("Monthly billing processing completed successfully.");
    }

    /**
     * Prices one tenant's usage over a window.
     *
     * <p>Usage rows carry the gateway's tenant identifier, which is this
     * service's {@link Tenant#getName()} — the gateway mints tenants and this
     * service records what they spent, so the name is the join, not the local
     * row id.
     *
     * <p>The window is aggregated in the database. Reading a billing period's
     * every request into heap to add up one column is the kind of thing that
     * works until a tenant gets busy.
     */
    public BigDecimal calculateTenantInvoice(Tenant tenant, Instant start, Instant end) {
        long totalTokens = usageMetricRepo
                .totals(tenant.getName(), start, end)
                .totalTokens();

        BigDecimal cost = BigDecimal.valueOf(totalTokens)
                .divide(BigDecimal.valueOf(1000))
                .multiply(COST_PER_THOUSAND_TOKENS);

        log.info("Invoice for Tenant: {} | Period: {} to {} | Total Tokens: {} | Total Cost: ${}",
                tenant.getName(), start, end, totalTokens, cost);

        return cost;
    }
}
