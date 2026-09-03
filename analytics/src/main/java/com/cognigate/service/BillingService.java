package com.cognigate.service;

import com.cognigate.repository.UsageMetricRepo;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

import java.math.BigDecimal;
import java.time.Duration;
import java.time.Instant;
import java.util.List;

/**
 * Prices a window of metered usage, per tenant.
 *
 * <p>The tenants are read out of the usage rows rather than from a tenant
 * table. The gateway is the authority on tenant identity — it mints them and
 * authenticates against its own store — and the only thing it tells this
 * service about a tenant is the identifier it stamps on each usage record.
 * Billing from a local tenant table would mean billing whoever had been
 * synchronised into it, which is not the same set as whoever generated traffic.
 *
 * <p>GW-4 keeps this outside its scope deliberately: CogniGate exposes numbers,
 * and presenting or charging them is the consumer's business. What runs here is
 * the aggregate, logged; there is no invoice document and no ledger.
 */
@Service
public class BillingService {

    private static final Logger log = LoggerFactory.getLogger(BillingService.class);

    private final UsageMetricRepo usageMetricRepo;

    // Price per 1K tokens ($)
    private static final BigDecimal COST_PER_THOUSAND_TOKENS = new BigDecimal("0.0015");

    public BillingService(UsageMetricRepo usageMetricRepo) {
        this.usageMetricRepo = usageMetricRepo;
    }

    /**
     * Run at midnight on the first day of every month to process billing.
     */
    @Scheduled(cron = "0 0 0 1 * ?")
    public void runMonthlyBilling() {
        log.info("Starting scheduled monthly billing processing...");
        Instant end = Instant.now();
        Instant start = end.minus(Duration.ofDays(30));

        List<String> tenantIds = usageMetricRepo.tenantIdsWithUsage(start, end);
        for (String tenantId : tenantIds) {
            calculateTenantInvoice(tenantId, start, end);
        }
        log.info("Monthly billing processing completed for {} tenant(s).", tenantIds.size());
    }

    /**
     * Prices one tenant's usage over a window.
     *
     * <p>The window is aggregated in the database. Reading a billing period's
     * every request into heap to add up one column is the kind of thing that
     * works until a tenant gets busy.
     */
    public BigDecimal calculateTenantInvoice(String tenantId, Instant start, Instant end) {
        long totalTokens = usageMetricRepo
                .totals(tenantId, start, end)
                .totalTokens();

        BigDecimal cost = BigDecimal.valueOf(totalTokens)
                .divide(BigDecimal.valueOf(1000))
                .multiply(COST_PER_THOUSAND_TOKENS);

        log.info("Invoice for Tenant: {} | Period: {} to {} | Total Tokens: {} | Total Cost: ${}",
                tenantId, start, end, totalTokens, cost);

        return cost;
    }
}
