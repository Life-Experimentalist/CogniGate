package com.cognigate.service;

import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.entity.Tenant;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.UsageMetricRepo;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.math.BigDecimal;
import java.time.Duration;
import java.time.Instant;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
@DisplayName("BillingService — Invoice Calculation Tests")
class BillingServiceTest {

    @Mock
    private TenantRepository tenantRepository;

    @Mock
    private UsageMetricRepo usageMetricRepo;

    @InjectMocks
    private BillingService billingService;

    private Tenant testTenant;

    @BeforeEach
    void setUp() {
        testTenant = new Tenant();
        testTenant.setId(1L);
        testTenant.setName("test-org");
        testTenant.setCognigateApiKey("cg-abc123");
    }

    private static UsageTotalsResponse totalling(long totalTokens) {
        return new UsageTotalsResponse(1L, 4000L, 6000L, totalTokens, new BigDecimal("0.015"));
    }

    @Test
    @DisplayName("calculateTenantInvoice() should return correct cost for token usage")
    void calculateInvoice_withTokenUsage_returnsCorrectCost() {
        // 10,000 total tokens * $0.0015 per 1K = $0.015
        when(usageMetricRepo.totals(eq("test-org"), any(), any()))
            .thenReturn(totalling(10_000L));

        Instant end = Instant.parse("2026-03-01T00:00:00Z");
        Instant start = end.minus(Duration.ofDays(30));

        BigDecimal cost = billingService.calculateTenantInvoice(testTenant, start, end);

        assertThat(cost).isEqualByComparingTo(new BigDecimal("0.01500"));
        verify(usageMetricRepo, times(1)).totals(eq("test-org"), eq(start), eq(end));
    }

    @Test
    @DisplayName("calculateTenantInvoice() should return zero for tenant with no usage")
    void calculateInvoice_withNoUsage_returnsZero() {
        // What an aggregate over an empty window actually returns: one row of nulls.
        when(usageMetricRepo.totals(any(), any(), any()))
            .thenReturn(new UsageTotalsResponse(0L, null, null, null, null));

        BigDecimal cost = billingService.calculateTenantInvoice(
            testTenant,
            Instant.parse("2026-02-01T00:00:00Z"),
            Instant.parse("2026-03-01T00:00:00Z")
        );

        assertThat(cost).isEqualByComparingTo(BigDecimal.ZERO);
    }

    @Test
    @DisplayName("calculateTenantInvoice() joins usage on the tenant's gateway name, not its row id")
    void calculateInvoice_queriesByTenantName() {
        when(usageMetricRepo.totals(any(), any(), any())).thenReturn(totalling(0L));

        billingService.calculateTenantInvoice(
            testTenant,
            Instant.parse("2026-02-01T00:00:00Z"),
            Instant.parse("2026-03-01T00:00:00Z")
        );

        // Usage rows carry the identifier the gateway authenticated against.
        // Billing on this service's local row id would price every tenant at zero.
        ArgumentCaptor<String> tenant = ArgumentCaptor.forClass(String.class);
        verify(usageMetricRepo).totals(tenant.capture(), any(), any());
        assertThat(tenant.getValue()).isEqualTo("test-org");
    }

    @Test
    @DisplayName("runMonthlyBilling() should process all tenants")
    void runMonthlyBilling_shouldProcessAllTenants() {
        when(tenantRepository.findAll()).thenReturn(List.of(testTenant));
        when(usageMetricRepo.totals(any(), any(), any())).thenReturn(totalling(0L));

        billingService.runMonthlyBilling();

        verify(tenantRepository, times(1)).findAll();
        verify(usageMetricRepo, times(1)).totals(any(), any(), any());
    }

    @Test
    @DisplayName("runMonthlyBilling() bills a half-open window ending now")
    void runMonthlyBilling_usesAThirtyDayWindow() {
        when(tenantRepository.findAll()).thenReturn(List.of(testTenant));
        when(usageMetricRepo.totals(any(), any(), any())).thenReturn(totalling(0L));

        Instant before = Instant.now();
        billingService.runMonthlyBilling();
        Instant after = Instant.now();

        ArgumentCaptor<Instant> start = ArgumentCaptor.forClass(Instant.class);
        ArgumentCaptor<Instant> end = ArgumentCaptor.forClass(Instant.class);
        verify(usageMetricRepo).totals(any(), start.capture(), end.capture());

        assertThat(end.getValue()).isBetween(before, after);
        assertThat(Duration.between(start.getValue(), end.getValue()))
            .isEqualTo(Duration.ofDays(30));
    }
}
