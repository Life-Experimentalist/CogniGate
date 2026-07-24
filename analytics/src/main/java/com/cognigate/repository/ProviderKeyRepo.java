package com.cognigate.repository;

import com.cognigate.entity.ProviderKey;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.List;

@Repository
public interface ProviderKeyRepo extends JpaRepository<ProviderKey, Long> {
    List<ProviderKey> findByTenantId(Long tenantId);
}
