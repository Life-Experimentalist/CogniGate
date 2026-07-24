package com.cognigate.repository;

import com.cognigate.entity.Tenant;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.Optional;

@Repository
public interface TenantRepository extends JpaRepository<Tenant, Long> {
    Optional<Tenant> findByCognigateApiKey(String apiKey);
    Optional<Tenant> findByName(String name);
}
