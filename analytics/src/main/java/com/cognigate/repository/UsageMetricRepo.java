package com.cognigate.repository;

import com.cognigate.entity.UsageMetric;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.time.LocalDateTime;
import java.util.List;

@Repository
public interface UsageMetricRepo extends JpaRepository<UsageMetric, Long> {
    List<UsageMetric> findByTenantIdAndRecordedAtBetween(Long tenantId, LocalDateTime start, LocalDateTime end);
}
