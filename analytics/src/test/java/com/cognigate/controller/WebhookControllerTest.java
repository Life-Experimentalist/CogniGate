package com.cognigate.controller;

import com.cognigate.entity.Tenant;
import com.cognigate.entity.UsageMetric;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.UsageMetricRepo;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest;
import org.springframework.boot.test.mock.mockito.MockBean;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;

import java.util.Optional;

import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

@WebMvcTest(WebhookController.class)
@DisplayName("WebhookController — Telemetry Ingestion Tests")
class WebhookControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockBean
    private TenantRepository tenantRepository;

    @MockBean
    private UsageMetricRepo usageMetricRepo;

    @Autowired
    private ObjectMapper objectMapper;

    private Tenant testTenant;

    @BeforeEach
    void setUp() {
        testTenant = new Tenant();
        testTenant.setId(1L);
        testTenant.setName("test-org");
        testTenant.setCognigateApiKey("cg-abc123");
    }

    @Test
    @DisplayName("POST /api/webhook/telemetry with valid tenant returns 200")
    void receiveTelemetry_withValidTenant_returns200() throws Exception {
        when(tenantRepository.findByName("test-org")).thenReturn(Optional.of(testTenant));
        when(usageMetricRepo.save(any(UsageMetric.class))).thenAnswer(i -> i.getArgument(0));

        String payload = """
            {
              "tenantId": "test-org",
              "promptTokens": 15,
              "completionTokens": 20,
              "totalTokens": 35
            }
            """;

        mockMvc.perform(post("/api/webhook/telemetry")
                .contentType(MediaType.APPLICATION_JSON)
                .content(payload))
            .andExpect(status().isOk())
            .andExpect(content().string("Telemetry recorded successfully"));

        verify(usageMetricRepo, times(1)).save(any(UsageMetric.class));
    }

    @Test
    @DisplayName("POST /api/webhook/telemetry with unknown tenant returns 404")
    void receiveTelemetry_withUnknownTenant_returns404() throws Exception {
        when(tenantRepository.findByName("unknown")).thenReturn(Optional.empty());

        String payload = """
            {
              "tenantId": "unknown",
              "promptTokens": 5,
              "completionTokens": 10,
              "totalTokens": 15
            }
            """;

        mockMvc.perform(post("/api/webhook/telemetry")
                .contentType(MediaType.APPLICATION_JSON)
                .content(payload))
            .andExpect(status().isNotFound());

        verify(usageMetricRepo, never()).save(any());
    }
}
