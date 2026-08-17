package com.cognigate.config;

import com.cognigate.controller.UsageController;
import com.cognigate.repository.UsageMetricRepo;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import org.springframework.context.annotation.Import;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

/**
 * The analytics service is published on a host port by the reference compose
 * deployment, and {@code POST /api/v1/usage} is the write that every invoice is
 * eventually computed from. These are the tests that say it is not open.
 */
@WebMvcTest(value = UsageController.class, properties = "ANALYTICS_TOKEN=" + ApiTokenFilterTest.TOKEN)
// A @WebMvcTest slice does not pick up a plain @Configuration class, so without
// this import the chain under test would simply be absent and every assertion
// below would pass against a service with no authentication at all.
@Import(SecurityConfig.class)
@DisplayName("ApiTokenFilter — /api/** is closed to callers without the shared token")
class ApiTokenFilterTest {

    static final String TOKEN = "a-token-for-tests";

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private UsageMetricRepo usageMetricRepo;

    private static final String RECORD = """
            {
              "request_id": "req_01",
              "client_request_id": "caller-abc",
              "tenant_id": "tnt_dev",
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
              "recorded_at": "2026-03-01T10:30:00Z"
            }
            """;

    @Test
    @DisplayName("an unauthenticated write is refused and never reaches the database")
    void record_withNoHeader_is401() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isUnauthorized())
                .andExpect(jsonPath("$.error").exists());

        // A 401 that still wrote the row would be worse than no check at all.
        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("a wrong token is refused")
    void record_withWrongToken_is401() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .header(HttpHeaders.AUTHORIZATION, "Bearer not-the-token")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isUnauthorized());

        verify(usageMetricRepo, never()).save(any());
    }

    @Test
    @DisplayName("a token that is merely a prefix of the real one is refused")
    void record_withTruncatedToken_is401() throws Exception {
        // The constant-time compare is only worth having if a short answer is
        // rejected on its length rather than accepted on its prefix.
        mockMvc.perform(post("/api/v1/usage")
                        .header(HttpHeaders.AUTHORIZATION, "Bearer " + TOKEN.substring(0, 5))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isUnauthorized());
    }

    @Test
    @DisplayName("the token presented under another scheme is refused")
    void record_withBasicScheme_is401() throws Exception {
        mockMvc.perform(post("/api/v1/usage")
                        .header(HttpHeaders.AUTHORIZATION, "Basic " + TOKEN)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isUnauthorized());
    }

    @Test
    @DisplayName("the gateway's own header reaches the controller")
    void record_withTheToken_reachesTheController() throws Exception {
        when(usageMetricRepo.existsByRequestId("req_01")).thenReturn(false);

        mockMvc.perform(post("/api/v1/usage")
                        .header(HttpHeaders.AUTHORIZATION, "Bearer " + TOKEN)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(RECORD))
                .andExpect(status().isCreated());

        verify(usageMetricRepo).save(any());
    }

    @Test
    @DisplayName("reads are closed too, not just the write")
    void totals_withNoHeader_is401() throws Exception {
        mockMvc.perform(get("/api/v1/usage/totals")
                        .param("tenant_id", "tnt_dev")
                        .param("since", "2026-03-01T00:00:00Z")
                        .param("until", "2026-03-02T00:00:00Z"))
                .andExpect(status().isUnauthorized());
    }

    @Test
    @DisplayName("the health endpoint stays open, because the container healthcheck has no token")
    void health_isNotRefused() throws Exception {
        // Not mapped in a web slice, so 404 is the honest answer here. What
        // matters is that the chain did not refuse it: a health endpoint behind
        // the token could never tell `compose up --wait` the service is up.
        int status = mockMvc.perform(get("/actuator/health")).andReturn().getResponse().getStatus();
        assertThat(status).isNotIn(401, 403);
    }

    @Test
    @DisplayName("a blank token is a startup failure, not a service that runs open")
    void construction_withoutAToken_fails() {
        // Defaulting would put the same public value on every deployment that
        // forgot the variable, which is indistinguishable from no check at all.
        assertThatThrownBy(() -> new SecurityConfig("  "))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("ANALYTICS_TOKEN");
    }
}
