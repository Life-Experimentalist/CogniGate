package com.cognigate.controller;

import com.cognigate.dto.UsageBucketResponse;
import com.cognigate.dto.UsageRecordRequest;
import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.entity.UsageMetric;
import com.cognigate.repository.UsageMetricRepo;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import org.springframework.dao.DataIntegrityViolationException;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

@WebMvcTest(UsageController.class)
@DisplayName("UsageController — durable usage ingest and aggregation")
class UsageControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private UsageMetricRepo usageMetricRepo;

    /**
     * A record in the wire shape the gateway actually sends, with the three
     * fields the controller insists on left to the caller as raw JSON values.
     *
     * <p>Everything else is always present because the sender always sends it,
     * and the tests below need the body to reach the controller's own checks
     * rather than stopping at the parser.
     */
    private static String recordJson(String requestId, String tenantId, String recordedAt) {
        return """
                {
                  "request_id": %s,
                  "client_request_id": "caller-abc",
                  "tenant_id": %s,
                  "key_prefix": "cg-dev-abcd",
                  "provider": "openai",
                  "model": "gpt-4o-mini",
                  "requested_model": "fast",
                  "fallback_depth": 1,
                  "prompt_tokens": 15,
                  "completion_tokens": 20,
                  "total_tokens": 35,
                  "cost_usd": 0.00042,
                  "cached": false,
                  "streamed": true,
                  "status_code": 200,
                  "duration_ms": 812,
                  "recorded_at": %s
                }
                """.formatted(requestId, tenantId, recordedAt);
    }

    private static final String RECORD =
            recordJson("\"req_01\"", "\"tnt_dev\"", "\"2026-03-01T10:30:00Z\"");

    @Test
    @DisplayName("a new record is stored whole and answered 201")
    void record_whenNew_persistsEveryFieldAnd201() throws Exception {
        when(usageMetricRepo.existsByRequestId("req_01")).thenReturn(false);

        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isCreated());

        ArgumentCaptor<UsageMetric> saved = ArgumentCaptor.forClass(UsageMetric.class);
        verify(usageMetricRepo).save(saved.capture());
        UsageMetric m = saved.getValue();

        assertThat(m.getRequestId()).isEqualTo("req_01");
        assertThat(m.getClientRequestId()).isEqualTo("caller-abc");
        assertThat(m.getTenantId()).isEqualTo("tnt_dev");
        assertThat(m.getKeyPrefix()).isEqualTo("cg-dev-abcd");
        assertThat(m.getProvider()).isEqualTo("openai");
        assertThat(m.getModel()).isEqualTo("gpt-4o-mini");
        assertThat(m.getRequestedModel()).isEqualTo("fast");
        assertThat(m.getFallbackDepth()).isEqualTo(1);
        assertThat(m.getPromptTokens()).isEqualTo(15);
        assertThat(m.getCompletionTokens()).isEqualTo(20);
        assertThat(m.getTotalTokens()).isEqualTo(35);
        assertThat(m.getCostUsd()).isEqualByComparingTo("0.00042");
        assertThat(m.getCached()).isFalse();
        assertThat(m.getStreamed()).isTrue();
        assertThat(m.getStatusCode()).isEqualTo(200);
        assertThat(m.getDurationMs()).isEqualTo(812L);
    }

    @Test
    @DisplayName("the gateway's own timestamp is stored, not the moment of arrival")
    void record_keepsTheSendersTimestamp() throws Exception {
        when(usageMetricRepo.existsByRequestId(any())).thenReturn(false);

        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isCreated());

        ArgumentCaptor<UsageMetric> saved = ArgumentCaptor.forClass(UsageMetric.class);
        verify(usageMetricRepo).save(saved.capture());

        // Stamping on receipt would put a record replayed after an outage in
        // whichever window the service came back up in. This is the assertion
        // that says it does not.
        assertThat(saved.getValue().getRecordedAt())
                .isEqualTo(Instant.parse("2026-03-01T10:30:00Z"));
    }

    @Test
    @DisplayName("a record already held is answered 200 and not stored twice")
    void record_whenAlreadyHeld_is200AndNotStoredAgain() throws Exception {
        when(usageMetricRepo.existsByRequestId("req_01")).thenReturn(true);

        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isOk());

        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("two deliveries racing on one record still yield 200, not 500")
    void record_whenDeliveriesRace_is200() throws Exception {
        // Both callers pass the pre-check, one loses at the unique constraint.
        when(usageMetricRepo.existsByRequestId("req_01")).thenReturn(false, true);
        when(usageMetricRepo.save(any())).thenThrow(new DataIntegrityViolationException("duplicate"));

        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isOk());
    }

    @Test
    @DisplayName("a constraint failure that is not a duplicate stays a failure")
    void record_whenConstraintFailureIsNotADuplicate_isNotSwallowed() {
        when(usageMetricRepo.existsByRequestId("req_01")).thenReturn(false, false);
        when(usageMetricRepo.save(any())).thenThrow(new DataIntegrityViolationException("value too long"));

        // Called directly rather than through MockMvc: what matters is that the
        // controller lets this out, not which of the container's error paths
        // then renders it. Swallowing it as a duplicate would lose the record
        // silently — the gateway must see a 5xx so that it retries.
        UsageController controller = new UsageController(usageMetricRepo);
        assertThatThrownBy(() -> controller.record(recordRequest("req_01")))
                .isInstanceOf(DataIntegrityViolationException.class);
    }

    private static UsageRecordRequest recordRequest(String requestId) {
        return new UsageRecordRequest(requestId, "caller-abc", "tnt_dev", "cg-dev-abcd",
                "openai", "gpt-4o-mini", "fast", 1, 15, 20, 35,
                new BigDecimal("0.00042"), false, true, 200, 812L,
                Instant.parse("2026-03-01T10:30:00Z"));
    }

    @Test
    @DisplayName("a record with no timestamp is refused rather than stamped on arrival")
    void record_withoutRecordedAt_is400() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(recordJson("\"req_02\"", "\"tnt_dev\"", "null")))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error").value("recorded_at is required."));

        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("a record with no request_id is refused, because nothing could deduplicate it")
    void record_withoutRequestId_is400() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(recordJson("null", "\"tnt_dev\"", "\"2026-03-01T10:30:00Z\"")))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error").value("request_id is required."));

        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("a record with no tenant is refused")
    void record_withoutTenantId_is400() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(recordJson("\"req_03\"", "\"  \"", "\"2026-03-01T10:30:00Z\"")))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error").value("tenant_id is required."));

        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("totals come back in the gateway's field names")
    void totals_areReportedInTheWireShape() throws Exception {
        when(usageMetricRepo.totals(eq("tnt_dev"), any(), any()))
                .thenReturn(new UsageTotalsResponse(3L, 30L, 45L, 75L, new BigDecimal("0.01500")));

        mockMvc.perform(get("/api/v1/usage/totals")
                        .param("tenant_id", "tnt_dev")
                        .param("since", "2026-03-01T00:00:00Z")
                        .param("until", "2026-03-02T00:00:00Z"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.requests").value(3))
                .andExpect(jsonPath("$.prompt_tokens").value(30))
                .andExpect(jsonPath("$.completion_tokens").value(45))
                .andExpect(jsonPath("$.total_tokens").value(75))
                .andExpect(jsonPath("$.cost_usd").value(0.015));
    }

    @Test
    @DisplayName("a window with no usage totals zero, not null")
    void totals_withNoRows_areZero() throws Exception {
        // An aggregate over an empty window returns one row of nulls; a tenant
        // that has sent nothing has used nothing, not an unknown amount.
        when(usageMetricRepo.totals(any(), any(), any()))
                .thenReturn(new UsageTotalsResponse(0L, null, null, null, null));

        mockMvc.perform(get("/api/v1/usage/totals")
                        .param("tenant_id", "tnt_quiet")
                        .param("since", "2026-03-01T00:00:00Z")
                        .param("until", "2026-03-02T00:00:00Z"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.requests").value(0))
                .andExpect(jsonPath("$.total_tokens").value(0))
                .andExpect(jsonPath("$.cost_usd").value(0));
    }

    @Test
    @DisplayName("key_prefix narrows the totals to one key")
    void totals_withKeyPrefix_useTheKeyQuery() throws Exception {
        when(usageMetricRepo.keyTotals(eq("tnt_dev"), eq("cg-dev-abcd"), any(), any()))
                .thenReturn(new UsageTotalsResponse(1L, 10L, 10L, 20L, new BigDecimal("0.002")));

        mockMvc.perform(get("/api/v1/usage/totals")
                        .param("tenant_id", "tnt_dev")
                        .param("key_prefix", "cg-dev-abcd")
                        .param("since", "2026-03-01T00:00:00Z")
                        .param("until", "2026-03-02T00:00:00Z"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.requests").value(1));

        verify(usageMetricRepo, never()).totals(any(), any(), any());
    }

    @Test
    @DisplayName("each supported grouping reaches its own query")
    void breakdown_routesEachGrouping() throws Exception {
        when(usageMetricRepo.breakdownByModel(any(), any(), any()))
                .thenReturn(List.of(new UsageBucketResponse(
                        "gpt-4o-mini", 2L, 20L, 30L, 50L, new BigDecimal("0.01"))));
        when(usageMetricRepo.breakdownByProvider(any(), any(), any())).thenReturn(List.of());
        when(usageMetricRepo.breakdownByKey(any(), any(), any())).thenReturn(List.of());
        when(usageMetricRepo.breakdownByClientRequestId(any(), any(), any())).thenReturn(List.of());

        mockMvc.perform(breakdownRequest("model"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$[0].key").value("gpt-4o-mini"))
                .andExpect(jsonPath("$[0].total_tokens").value(50));

        mockMvc.perform(breakdownRequest("provider")).andExpect(status().isOk());
        mockMvc.perform(breakdownRequest("key")).andExpect(status().isOk());
        mockMvc.perform(breakdownRequest("client_request_id")).andExpect(status().isOk());

        verify(usageMetricRepo).breakdownByModel(any(), any(), any());
        verify(usageMetricRepo).breakdownByProvider(any(), any(), any());
        verify(usageMetricRepo).breakdownByKey(any(), any(), any());
        verify(usageMetricRepo).breakdownByClientRequestId(any(), any(), any());
    }

    @Test
    @DisplayName("an unsupported grouping is refused, and reaches no query")
    void breakdown_withUnknownGrouping_is400() throws Exception {
        mockMvc.perform(breakdownRequest("tenant"))
                .andExpect(status().isBadRequest());

        verifyNoInteractions(usageMetricRepo);
    }

    @Test
    @DisplayName("an unparseable window is refused rather than retried forever")
    void totals_withUnparseableWindow_is400() throws Exception {
        // A 4xx is what tells the gateway to drop the request instead of
        // replaying it; a 500 here would wedge its queue.
        mockMvc.perform(get("/api/v1/usage/totals")
                        .param("tenant_id", "tnt_dev")
                        .param("since", "last tuesday")
                        .param("until", "2026-03-02T00:00:00Z"))
                .andExpect(status().isBadRequest());
    }

    private static org.springframework.test.web.servlet.RequestBuilder breakdownRequest(String groupBy) {
        return get("/api/v1/usage/breakdown")
                .param("tenant_id", "tnt_dev")
                .param("group_by", groupBy)
                .param("since", "2026-03-01T00:00:00Z")
                .param("until", "2026-03-02T00:00:00Z");
    }
}
