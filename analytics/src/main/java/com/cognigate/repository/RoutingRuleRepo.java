package com.cognigate.repository;

import com.cognigate.entity.RoutingRule;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.List;

@Repository
public interface RoutingRuleRepo extends JpaRepository<RoutingRule, Long> {
    List<RoutingRule> findByTenantIdOrderByPriorityAsc(Long tenantId);
}
