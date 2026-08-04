package com.cognigate.service;

import com.cognigate.entity.Tenant;
import com.cognigate.entity.UsageMetric;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.UsageMetricRepo;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.math.BigDecimal;
import java.time.LocalDateTime;
import java.util.Collections;
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

    @Test
    @DisplayName("calculateTenantInvoice() should return correct cost for token usage")
    void calculateInvoice_withTokenUsage_returnsCorrectCost() {
        // 10,000 total tokens * $0.0015 per 1K = $0.015
        UsageMetric metric = new UsageMetric();
        metric.setTenant(testTenant);
        metric.setPromptTokens(4000);
        metric.setCompletionTokens(6000);
        metric.setTotalTokens(10000);
        metric.setRecordedAt(LocalDateTime.now());

        LocalDateTime start = LocalDateTime.now().minusMonths(1);
        LocalDateTime end = LocalDateTime.now();

        when(usageMetricRepo.findByTenantIdAndRecordedAtBetween(eq(1L), any(), any()))
            .thenReturn(List.of(metric));

        BigDecimal cost = billingService.calculateTenantInvoice(testTenant, start, end);

        assertThat(cost).isEqualByComparingTo(new BigDecimal("0.01500"));
        verify(usageMetricRepo, times(1)).findByTenantIdAndRecordedAtBetween(eq(1L), any(), any());
    }

    @Test
    @DisplayName("calculateTenantInvoice() should return zero for tenant with no usage")
    void calculateInvoice_withNoUsage_returnsZero() {
        when(usageMetricRepo.findByTenantIdAndRecordedAtBetween(any(), any(), any()))
            .thenReturn(Collections.emptyList());

        BigDecimal cost = billingService.calculateTenantInvoice(
            testTenant,
            LocalDateTime.now().minusMonths(1),
            LocalDateTime.now()
        );

        assertThat(cost).isEqualByComparingTo(BigDecimal.ZERO);
    }

    @Test
    @DisplayName("runMonthlyBilling() should process all tenants")
    void runMonthlyBilling_shouldProcessAllTenants() {
        when(tenantRepository.findAll()).thenReturn(List.of(testTenant));
        when(usageMetricRepo.findByTenantIdAndRecordedAtBetween(any(), any(), any()))
            .thenReturn(Collections.emptyList());

        billingService.runMonthlyBilling();

        verify(tenantRepository, times(1)).findAll();
        verify(usageMetricRepo, times(1)).findByTenantIdAndRecordedAtBetween(any(), any(), any());
    }
}
