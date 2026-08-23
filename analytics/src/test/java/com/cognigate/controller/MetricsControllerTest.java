package com.cognigate.controller;

import io.micrometer.core.instrument.Counter;
import io.micrometer.prometheusmetrics.PrometheusConfig;
import io.micrometer.prometheusmetrics.PrometheusMeterRegistry;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.test.web.servlet.MockMvc;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

/**
 * GW-8 requires {@code GET /metrics} in Prometheus text format on both
 * processes. The slice supplies its own registry: {@code @WebMvcTest} loads web
 * layer beans only, so the one Spring Boot autoconfigures from the classpath in
 * a running application is not present here.
 */
@WebMvcTest(MetricsController.class)
@Import(MetricsControllerTest.RegistryConfig.class)
@DisplayName("MetricsController — the analytics engine's Prometheus scrape")
class MetricsControllerTest {

    @TestConfiguration
    static class RegistryConfig {
        @Bean
        PrometheusMeterRegistry prometheusMeterRegistry() {
            return new PrometheusMeterRegistry(PrometheusConfig.DEFAULT);
        }
    }

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private PrometheusMeterRegistry registry;

    @Test
    @DisplayName("renders the registry in the text exposition format")
    void rendersExposition() throws Exception {
        Counter.builder("cognigate_analytics_test_total")
                .description("A series the assertion can look for by name.")
                .register(registry)
                .increment();

        mockMvc.perform(get("/metrics"))
                .andExpect(status().isOk())
                // A Prometheus server dispatches on this; actuator's own JSON at
                // the same path would satisfy "200" and nothing else.
                .andExpect(content().contentTypeCompatibleWith("text/plain"))
                .andExpect(content().string(
                        org.hamcrest.Matchers.containsString("cognigate_analytics_test_total")))
                // The HELP/TYPE preamble is what makes it the exposition format
                // rather than a list of numbers that happens to be text.
                .andExpect(content().string(
                        org.hamcrest.Matchers.containsString("# TYPE cognigate_analytics_test_total")));
    }
}
