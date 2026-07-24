package com.cognigate.service;

import com.cognigate.entity.Tenant;
import com.cognigate.entity.RoutingRule;
import com.cognigate.entity.ProviderKey;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.stereotype.Service;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;

@Service
public class CacheSyncService {

    private final StringRedisTemplate redisTemplate;
    private final EncryptionService encryptionService;
    private final ObjectMapper objectMapper;

    public CacheSyncService(StringRedisTemplate redisTemplate,
                            EncryptionService encryptionService,
                            ObjectMapper objectMapper) {
        this.redisTemplate = redisTemplate;
        this.encryptionService = encryptionService;
        this.objectMapper = objectMapper;
    }

    /**
     * Syncs a tenant's config (decrypted keys & active rules) to Redis
     * and broadcasts a cache invalidation event.
     */
    public void syncTenantToRedis(Tenant tenant) {
        try {
            Map<String, Object> config = new HashMap<>();
            config.put("tenant_id", tenant.getName());

            // Map routing rules
            List<Map<String, Object>> rules = tenant.getRoutingRules().stream().map(rule -> {
                Map<String, Object> r = new HashMap<>();
                r.put("model", rule.getModelName());
                r.put("backup_model", rule.getBackupModelName());
                r.put("priority", rule.getPriority());
                return r;
            }).collect(Collectors.toList());
            config.put("routing_rules", rules);

            // Decrypt and map provider keys
            Map<String, String> decryptedKeys = new HashMap<>();
            for (ProviderKey pk : tenant.getProviderKeys()) {
                String decrypted = encryptionService.decrypt(pk.getEncryptedApiKey());
                decryptedKeys.put(pk.getProviderName(), decrypted);
            }
            config.put("provider_keys", decryptedKeys);

            String jsonConfig = objectMapper.writeValueAsString(config);
            String cacheKey = "tenant:cfg:" + tenant.getCognigateApiKey();

            // 1. Set key in Redis
            redisTemplate.opsForValue().set(cacheKey, jsonConfig);

            // 2. Publish invalidation event
            redisTemplate.convertAndSend("cognigate:cache:invalidate", tenant.getCognigateApiKey());

        } catch (Exception e) {
            throw new RuntimeException("Failed to sync tenant cache to Redis", e);
        }
    }
}
