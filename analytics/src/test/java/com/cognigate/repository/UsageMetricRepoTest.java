package com.cognigate.repository;

import com.cognigate.dto.UsageBucketResponse;
import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.entity.UsageMetric;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.TestPropertySource;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

/**
 * The only test that reads the repository's JPQL.
 *
 * <p>Every other test in this module mocks {@link UsageMetricRepo}, so a
 * constructor expression that names a column that does not exist, or passes its
 * arguments in the wrong order, compiles and passes the suite and then fails on
 * the first request after a deploy. This one boots a context against an
 * in-memory database, so each query is parsed, translated and executed.
 *
 * <p>H2 rather than the Postgres the service runs on: the aggregations here are
 * plain JPQL with no vendor syntax, and what is being checked is that the
 * queries are valid at all.
 */
@SpringBootTest
@TestPropertySource(properties = {
        "spring.datasource.url=jdbc:h2:mem:usagemetricrepo;DB_CLOSE_DELAY=-1",
        "spring.datasource.username=sa",
        "spring.datasource.password=",
        "spring.datasource.driver-class-name=org.h2.Driver",
        "spring.jpa.hibernate.ddl-auto=create-drop",
        "spring.jpa.properties.hibernate.dialect=org.hibernate.dialect.H2Dialect",
        // SecurityConfig refuses to construct without one, and a context that
        // will not refresh cannot run a query. Obviously not a real token.
        "ANALYTICS_TOKEN=not-a-real-token-local-only"
})
@DisplayName("UsageMetricRepo aggregation queries")
class UsageMetricRepoTest {

    @Autowired
    private UsageMetricRepo repo;

    /** A cached row carries no tokens and no cost, which is the point of it. */
    private static UsageMetric row(String id, boolean cached) {
        UsageMetric m = new UsageMetric();
        m.setRequestId(id);
        m.setClientRequestId("job-1");
        m.setTenantId("t1");
        m.setKeyPrefix("cg-a");
        m.setProvider("primary");
        m.setModel("test-small");
        m.setRequestedModel("test-small");
        m.setFallbackDepth(0);
        m.setPromptTokens(cached ? 0 : 10);
        m.setCompletionTokens(cached ? 0 : 5);
        m.setTotalTokens(cached ? 0 : 15);
        m.setCostUsd(cached ? BigDecimal.ZERO : new BigDecimal("0.01"));
        m.setChargeUsd(cached ? BigDecimal.ZERO : new BigDecimal("0.012"));
        m.setBillingMode("markup");
        m.setCached(cached);
        m.setStreamed(false);
        m.setStatusCode(200);
        m.setDurationMs(12L);
        m.setRecordedAt(Instant.parse("2026-03-01T10:00:00Z"));
        return m;
    }

    @Test
    @DisplayName("all six run, and a cache hit counts once in requests and once as cached")
    void everyQueryParsesAndCountsTheCachedRows() {
        repo.save(row("r1", false));
        repo.save(row("r2", true));

        Instant since = Instant.parse("2026-03-01T00:00:00Z");
        Instant until = Instant.parse("2026-03-02T00:00:00Z");

        UsageTotalsResponse t = repo.totals("t1", since, until);
        assertThat(t.requests()).isEqualTo(2L);
        assertThat(t.cachedRequests()).isEqualTo(1L);
        assertThat(t.totalTokens()).isEqualTo(15L);

        assertThat(repo.keyTotals("t1", "cg-a", since, until).cachedRequests()).isEqualTo(1L);

        for (List<UsageBucketResponse> rows : List.of(
                repo.breakdownByModel("t1", since, until),
                repo.breakdownByProvider("t1", since, until),
                repo.breakdownByKey("t1", since, until),
                repo.breakdownByClientRequestId("t1", since, until))) {
            assertThat(rows).hasSize(1);
            assertThat(rows.get(0).requests()).isEqualTo(2L);
            assertThat(rows.get(0).cachedRequests()).isEqualTo(1L);
        }
    }
}
