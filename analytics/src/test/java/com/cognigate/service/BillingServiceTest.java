package com.cognigate.service;

import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.repository.UsageMetricRepo;
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
    private UsageMetricRepo usageMetricRepo;

    @InjectMocks
    private BillingService billingService;

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

        BigDecimal cost = billingService.calculateTenantInvoice("test-org", start, end);

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
            "test-org",
            Instant.parse("2026-02-01T00:00:00Z"),
            Instant.parse("2026-03-01T00:00:00Z")
        );

        assertThat(cost).isEqualByComparingTo(BigDecimal.ZERO);
    }

    @Test
    @DisplayName("runMonthlyBilling() bills every tenant that sent traffic in the window")
    void runMonthlyBilling_billsTenantsDerivedFromUsage() {
        when(usageMetricRepo.tenantIdsWithUsage(any(), any()))
            .thenReturn(List.of("test-org", "other-org"));
        when(usageMetricRepo.totals(any(), any(), any())).thenReturn(totalling(0L));

        billingService.runMonthlyBilling();

        // The tenant set comes from the usage rows themselves. Reading it from a
        // tenant table would bill whoever had been synchronised into one, which
        // is not the same set as whoever generated traffic.
        ArgumentCaptor<String> tenant = ArgumentCaptor.forClass(String.class);
        verify(usageMetricRepo, times(2)).totals(tenant.capture(), any(), any());
        assertThat(tenant.getAllValues()).containsExactly("test-org", "other-org");
    }

    @Test
    @DisplayName("runMonthlyBilling() over a window nobody used calls nothing")
    void runMonthlyBilling_withNoUsage_pricesNothing() {
        when(usageMetricRepo.tenantIdsWithUsage(any(), any())).thenReturn(List.of());

        billingService.runMonthlyBilling();

        verify(usageMetricRepo, never()).totals(any(), any(), any());
    }

    @Test
    @DisplayName("runMonthlyBilling() bills a half-open window ending now")
    void runMonthlyBilling_usesAThirtyDayWindow() {
        when(usageMetricRepo.tenantIdsWithUsage(any(), any())).thenReturn(List.of("test-org"));
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
