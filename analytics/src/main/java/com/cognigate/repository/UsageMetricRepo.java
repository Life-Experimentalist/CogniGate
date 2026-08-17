package com.cognigate.repository;

import com.cognigate.dto.UsageBucketResponse;
import com.cognigate.dto.UsageTotalsResponse;
import com.cognigate.entity.UsageMetric;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.data.jpa.repository.Query;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;

import java.time.Instant;
import java.util.List;

/**
 * Usage storage.
 *
 * <p>Every window here is half-open — {@code recordedAt >= since} and
 * {@code recordedAt < until} — which is the gateway's own rule. A record on a
 * boundary must fall in exactly one of two adjacent windows, or a day's usage
 * would be counted twice by anything that walks a month a day at a time.
 *
 * <p>The aggregations are done by the database and returned already shaped.
 * Summing in Java would mean loading a billing period's every request into
 * heap to produce five numbers.
 */
@Repository
public interface UsageMetricRepo extends JpaRepository<UsageMetric, Long> {

    /**
     * Reports whether this record has already been stored, so a delivery the
     * gateway retried is recognised before it reaches the unique constraint.
     */
    boolean existsByRequestId(String requestId);

    @Query("""
            select new com.cognigate.dto.UsageTotalsResponse(
                count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId
              and u.recordedAt >= :since and u.recordedAt < :until
            """)
    UsageTotalsResponse totals(@Param("tenantId") String tenantId,
                               @Param("since") Instant since,
                               @Param("until") Instant until);

    @Query("""
            select new com.cognigate.dto.UsageTotalsResponse(
                count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId and u.keyPrefix = :keyPrefix
              and u.recordedAt >= :since and u.recordedAt < :until
            """)
    UsageTotalsResponse keyTotals(@Param("tenantId") String tenantId,
                                  @Param("keyPrefix") String keyPrefix,
                                  @Param("since") Instant since,
                                  @Param("until") Instant until);

    // The four breakdowns below are one query each rather than one query with
    // the grouping column passed in, because a column name cannot be a bind
    // parameter. Writing them out is what keeps the caller's group_by a value
    // this repository never interpolates into SQL.
    //
    // All four sort by spend descending, then by key, because the rows an
    // operator opened this endpoint to find are the expensive ones — and the
    // tie-break keeps the order stable across two calls that see the same data.

    @Query("""
            select new com.cognigate.dto.UsageBucketResponse(
                u.model, count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId
              and u.recordedAt >= :since and u.recordedAt < :until
            group by u.model
            order by sum(u.costUsd) desc, u.model asc
            """)
    List<UsageBucketResponse> breakdownByModel(@Param("tenantId") String tenantId,
                                               @Param("since") Instant since,
                                               @Param("until") Instant until);

    @Query("""
            select new com.cognigate.dto.UsageBucketResponse(
                u.provider, count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId
              and u.recordedAt >= :since and u.recordedAt < :until
            group by u.provider
            order by sum(u.costUsd) desc, u.provider asc
            """)
    List<UsageBucketResponse> breakdownByProvider(@Param("tenantId") String tenantId,
                                                  @Param("since") Instant since,
                                                  @Param("until") Instant until);

    @Query("""
            select new com.cognigate.dto.UsageBucketResponse(
                u.keyPrefix, count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId
              and u.recordedAt >= :since and u.recordedAt < :until
            group by u.keyPrefix
            order by sum(u.costUsd) desc, u.keyPrefix asc
            """)
    List<UsageBucketResponse> breakdownByKey(@Param("tenantId") String tenantId,
                                             @Param("since") Instant since,
                                             @Param("until") Instant until);

    /**
     * Records the caller never labelled are left out rather than piled into an
     * empty-keyed row. This grouping answers "what did the request I called
     * abc123 cost"; an unlabelled bucket answers nothing, and being the largest
     * row by spend it would sort to the top of every response.
     */
    @Query("""
            select new com.cognigate.dto.UsageBucketResponse(
                u.clientRequestId, count(u), sum(u.promptTokens), sum(u.completionTokens),
                sum(u.totalTokens), sum(u.costUsd))
            from UsageMetric u
            where u.tenantId = :tenantId
              and u.recordedAt >= :since and u.recordedAt < :until
              and u.clientRequestId is not null and u.clientRequestId <> ''
            group by u.clientRequestId
            order by sum(u.costUsd) desc, u.clientRequestId asc
            """)
    List<UsageBucketResponse> breakdownByClientRequestId(@Param("tenantId") String tenantId,
                                                         @Param("since") Instant since,
                                                         @Param("until") Instant until);
}
