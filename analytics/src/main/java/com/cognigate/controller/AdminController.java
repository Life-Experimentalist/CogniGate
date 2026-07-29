package com.cognigate.controller;

import com.cognigate.entity.Tenant;
import com.cognigate.entity.ProviderKey;
import com.cognigate.entity.RoutingRule;
import com.cognigate.repository.TenantRepository;
import com.cognigate.repository.ProviderKeyRepo;
import com.cognigate.repository.RoutingRuleRepo;
import com.cognigate.plugin.PluginManager;
import com.cognigate.service.CacheSyncService;
import com.cognigate.service.EncryptionService;
import lombok.Data;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.multipart.MultipartFile;

import java.util.List;
import java.util.Optional;
import java.util.UUID;

@RestController
@RequestMapping("/api/admin")
public class AdminController {

    private final TenantRepository tenantRepository;
    private final ProviderKeyRepo providerKeyRepo;
    private final RoutingRuleRepo routingRuleRepo;
    private final EncryptionService encryptionService;
    private final CacheSyncService cacheSyncService;
    private final PluginManager pluginManager;

    public AdminController(TenantRepository tenantRepository,
                           ProviderKeyRepo providerKeyRepo,
                           RoutingRuleRepo routingRuleRepo,
                           EncryptionService encryptionService,
                           CacheSyncService cacheSyncService,
                           PluginManager pluginManager) {
        this.tenantRepository = tenantRepository;
        this.providerKeyRepo = providerKeyRepo;
        this.routingRuleRepo = routingRuleRepo;
        this.encryptionService = encryptionService;
        this.cacheSyncService = cacheSyncService;
        this.pluginManager = pluginManager;
    }

    @PostMapping("/tenants")
    public ResponseEntity<Tenant> createTenant(@RequestParam String name) {
        Tenant tenant = new Tenant();
        tenant.setName(name);
        tenant.setCognigateApiKey("cg-" + UUID.randomUUID().toString().replace("-", ""));
        Tenant saved = tenantRepository.save(tenant);
        return ResponseEntity.ok(saved);
    }

    @GetMapping("/tenants")
    public ResponseEntity<List<Tenant>> listTenants() {
        return ResponseEntity.ok(tenantRepository.findAll());
    }

    @PostMapping("/tenants/{tenantId}/keys")
    public ResponseEntity<?> addProviderKey(@PathVariable Long tenantId, @RequestBody KeyRequest request) {
        Optional<Tenant> tenantOpt = tenantRepository.findById(tenantId);
        if (tenantOpt.isEmpty()) {
            return ResponseEntity.notFound().build();
        }

        Tenant tenant = tenantOpt.get();
        ProviderKey providerKey = new ProviderKey();
        providerKey.setTenant(tenant);
        providerKey.setProviderName(request.getProviderName());
        providerKey.setEncryptedApiKey(encryptionService.encrypt(request.getApiKey()));

        providerKeyRepo.save(providerKey);

        // Fetch fully populated tenant object with relations
        Tenant updatedTenant = tenantRepository.findById(tenantId).orElseThrow();
        cacheSyncService.syncTenantToRedis(updatedTenant);

        return ResponseEntity.ok("Provider key added and synced to cache.");
    }

    @PostMapping("/tenants/{tenantId}/rules")
    public ResponseEntity<?> addRoutingRule(@PathVariable Long tenantId, @RequestBody RuleRequest request) {
        Optional<Tenant> tenantOpt = tenantRepository.findById(tenantId);
        if (tenantOpt.isEmpty()) {
            return ResponseEntity.notFound().build();
        }

        Tenant tenant = tenantOpt.get();
        RoutingRule rule = new RoutingRule();
        rule.setTenant(tenant);
        rule.setModelName(request.getModelName());
        rule.setBackupModelName(request.getBackupModelName());
        rule.setPriority(request.getPriority());

        routingRuleRepo.save(rule);

        // Sync changes to Cache
        Tenant updatedTenant = tenantRepository.findById(tenantId).orElseThrow();
        cacheSyncService.syncTenantToRedis(updatedTenant);

        return ResponseEntity.ok("Routing rule added and synced to cache.");
    }

    @PostMapping("/plugins/upload")
    public ResponseEntity<?> uploadPlugin(@RequestParam("file") MultipartFile file, @RequestParam("className") String className) {
        if (file.isEmpty()) {
            return ResponseEntity.badRequest().body("File is empty");
        }
        try {
            String sourceCode = new String(file.getBytes(), java.nio.charset.StandardCharsets.UTF_8);
            pluginManager.loadPlugin(className, sourceCode);
            return ResponseEntity.ok("Plugin class " + className + " compiled and loaded successfully.");
        } catch (Exception e) {
            return ResponseEntity.status(500).body("Dynamic compilation failed: " + e.getMessage());
        }
    }

    @Data
    public static class KeyRequest {
        private String providerName;
        private String apiKey;
    }

    @Data
    public static class RuleRequest {
        private String modelName;
        private String backupModelName;
        private int priority;
    }
}
