# =============================================================================
# CogniGate — AI Context Document
# Version: 1.0.0
# Repository: https://github.com/Life-Experimentalist/CogniGate
# License: Apache 2.0 — Copyright 2026 VKrishna04 and Life Experimentalist
# =============================================================================
# PURPOSE: When the raw URL of this file is provided to an AI assistant,
#          the assistant should have complete knowledge of CogniGate's
#          architecture, configuration, troubleshooting, and customization.
# RAW URL: https://raw.githubusercontent.com/Life-Experimentalist/CogniGate/main/COGNIGATE_AI_CONTEXT.md
# =============================================================================

## 1. Project Overview

CogniGate is a **self-hosted, multi-tenant AI infrastructure platform** — a private enterprise alternative to OpenRouter and LiteLLM.

**Core Value Propositions:**
- **Zero-latency routing** via a Go 1.26 edge proxy (Fiber v2)
- **Zero-downtime key rotation** and circuit-breaker failover
- **Zero-trust encryption** for all provider API keys (AES-256-GCM)
- **Hot-swap plugin system** using Janino in-memory Java compilation

**Repository:** `https://github.com/Life-Experimentalist/CogniGate`  
**GitHub Pages:** `https://life-experimentalist.github.io/CogniGate`  
**License:** Apache 2.0

---

## 2. Architecture

### Polyglot System Design

```
CogniGate/
├── gateway/          # Go 1.26 + Fiber v2 — Edge Proxy on :8080
├── analytics/        # Java 26 + Spring Boot 4.1 — Domain Engine on :8081
├── docs/             # Next.js 15 — GitHub Pages documentation site
├── docker-compose.yml
├── setup.sh / setup.ps1  # One-command setup
├── cognigate.config.yml  # Unified config reference
└── .env.example          # All environment variables
```

### Component Interaction Flow

```
[Client] --POST /v1/chat/completions + Bearer cg-xxx-->
         [gateway :8080]
              |
              |--> Redis GET tenant:cfg:{key}  --> [Redis :6379]
              |
              |--> (round-robin key rotation)
              |    (exponential backoff checks)
              |    (circuit breaker)
              |
              |--> POST to LLM provider
              |
              |--> goroutine: POST /api/webhook/telemetry --> [analytics :8081]
              |
              <-- JSON response (OpenAI format)

[Admin] --POST /api/admin/*--> [analytics :8081]
              |
              |--> save to [PostgreSQL :5432]
              |--> sync to [Redis :6379]
              |--> publish cognigate:cache:invalidate
```

### Data Stores

| Store | Role | Image |
|---|---|---|
| PostgreSQL 16 | Source of truth — tenants, encrypted keys, routing rules, billing | `postgres:16-alpine` |
| Redis 7 | Fast-path config cache, Pub/Sub invalidation, rate-limit buckets | `redis:7-alpine` |

---

## 3. Environment Variables

All environment variables are documented in `.env.example`. Key ones:

| Variable | Component | Description |
|---|---|---|
| `REDIS_URL` | gateway | Redis address (host:port, no protocol) |
| `SPRING_DATASOURCE_URL` | analytics | JDBC URL for PostgreSQL |
| `SPRING_DATASOURCE_USERNAME` | analytics | DB username |
| `SPRING_DATASOURCE_PASSWORD` | analytics | DB password |
| `ENCRYPTION_MASTER_KEY` | analytics | AES-256-GCM key (64-char hex) |
| `POSTGRES_DB` | postgres | Database name |
| `POSTGRES_USER` | postgres | DB user |
| `POSTGRES_PASSWORD` | postgres | DB password |

**Generate a secure encryption key:**
```bash
openssl rand -hex 32
```

---

## 4. One-Command Setup

**Linux / macOS:**
```bash
./setup.sh --dev --detach
```

**Windows (PowerShell):**
```powershell
.\setup.ps1 -Mode dev -Detach
```

**Flags:**
- `--dev` / `-Mode dev`: Development mode (verbose logging)
- `--prod` / `-Mode prod`: Production mode
- `--detach` / `-Detach`: Run in background
- `--clean` / `-Clean`: Wipe data volumes before start

The setup scripts:
1. Copy `.env.example` → `.env` if not present
2. Auto-generate `ENCRYPTION_MASTER_KEY` if placeholder
3. Run `docker-compose up --build`

---

## 5. API Reference

### Edge Proxy (`:8080`)

#### `POST /v1/chat/completions`
OpenAI-compatible chat completion endpoint.

**Request:**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <cognigate-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Response (200 OK):**
```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4",
  "choices": [{
    "message": {"role": "assistant", "content": "..."},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 15,
    "completion_tokens": 20,
    "total_tokens": 35
  }
}
```

**Auth:** Bearer token must match a `cognigateApiKey` in PostgreSQL (prefixed `cg-`).  
**Special:** Use `Bearer test` to bypass Redis check in development.

#### `GET /health`
Returns `200 OK` with body `OK` if the gateway is running.

---

### Domain Engine (`:8081`)

#### `POST /api/admin/tenants`
Creates a new tenant and auto-generates a `cognigateApiKey`.

```bash
curl -X POST "http://localhost:8081/api/admin/tenants?name=my-org"
```

**Response:**
```json
{
  "id": 1,
  "name": "my-org",
  "cognigateApiKey": "cg-abc123..."
}
```

#### `POST /api/admin/tenants/{id}/keys`
Encrypts and stores a provider API key for a tenant.

```bash
curl -X POST http://localhost:8081/api/admin/tenants/1/keys \
  -H "Content-Type: application/json" \
  -d '{"providerName": "anthropic", "apiKey": "sk-ant-prod-..."}'
```

#### `POST /api/admin/tenants/{id}/rules`
Adds a priority routing rule with failover cascade.

```bash
curl -X POST http://localhost:8081/api/admin/tenants/1/rules \
  -H "Content-Type: application/json" \
  -d '{
    "modelName": "claude-3-opus",
    "backupModelName": "gpt-4-turbo",
    "priority": 1
  }'
```

#### `POST /api/admin/plugins/upload`
Uploads a `.java` file for Janino in-memory compilation.

```bash
curl -X POST http://localhost:8081/api/admin/plugins/upload \
  -F "file=@MyHandler.java" \
  -F "className=com.cognigate.plugin.MyHandler"
```

#### `POST /api/webhook/telemetry`
Receives async token usage metrics from the Go proxy.

```bash
curl -X POST http://localhost:8081/api/webhook/telemetry \
  -H "Content-Type: application/json" \
  -d '{"tenantId":"my-org","promptTokens":15,"completionTokens":20,"totalTokens":35}'
```

---

## 6. Database Schema

Auto-generated by Hibernate (`spring.jpa.hibernate.ddl-auto: update`).

### `tenant`
| Column | Type | Description |
|---|---|---|
| id | bigint PK | Auto-generated |
| name | varchar(255) UNIQUE | Organization name |
| cognigate_api_key | varchar(255) UNIQUE | `cg-` prefixed bearer token |

### `provider_key`
| Column | Type | Description |
|---|---|---|
| id | bigint PK | Auto-generated |
| provider_name | varchar(255) | e.g. "anthropic", "openai" |
| encrypted_api_key | varchar(1024) | AES-256-GCM + Base64 encoded |
| tenant_id | bigint FK → tenant.id | |

### `routing_rule`
| Column | Type | Description |
|---|---|---|
| id | bigint PK | |
| model_name | varchar(255) | Primary model to route to |
| backup_model_name | varchar(255) | Cascade fallback |
| priority | integer | Lower = higher priority |
| tenant_id | bigint FK → tenant.id | |

### `usage_metric`
| Column | Type | Description |
|---|---|---|
| id | bigint PK | |
| prompt_tokens | integer | |
| completion_tokens | integer | |
| total_tokens | integer | |
| recorded_at | timestamp | |
| tenant_id | bigint FK → tenant.id | |

---

## 7. Plugin System

### Tier 1: JSON Mapper (No-Code)
For OpenAI-compatible providers (Groq, TogetherAI, vLLM):
```bash
curl -X POST http://localhost:8081/api/admin/plugins/upload \
  -F "file=@groq.json" \
  -F "className=json-mapper"
```

JSON config format:
```json
{"baseUrl": "https://api.groq.com/v1", "authHeaderFormat": "Bearer %s"}
```

### Tier 2: Dynamic Java Compilation
For custom providers requiring complex logic (AWS Bedrock SigV4 signing, etc.):

```java
// MyCustomHandler.java — upload via /api/admin/plugins/upload
package com.cognigate.plugin;

import java.net.URI;
import java.net.http.*;

public class MyCustomHandler implements AiProviderHandler {
    @Override
    public String handleRequest(String prompt, String apiKey) throws Exception {
        // Custom HTTP logic here
        return "response";
    }
}
```

**Constraints:**
- Must implement `com.cognigate.plugin.AiProviderHandler`
- Must be stateless (shared across virtual threads)
- Compiled with Janino 3.1.12 (subset of Java spec)
- Class is loaded in an isolated `ClassLoader` (Metaspace-safe)

---

## 8. Redis Cache Structure

| Key Pattern | Type | Content |
|---|---|---|
| `tenant:cfg:{cognigateApiKey}` | String | JSON: routing rules + decrypted keys |
| `cognigate:cache:invalidate` | Pub/Sub channel | Payload: invalidated `cognigateApiKey` |

**Cache TTL:** 5 minutes (used in Redis-unreachable fallback mode)

---

## 9. Security Model

1. **Tenant isolation:** All routing rules and provider keys are scoped to tenants
2. **Encryption at rest:** Provider API keys are encrypted with AES-256-GCM before DB storage; decrypted only in-memory at cache-sync time
3. **Rate limiting:** Per-tenant RPS limits enforced at gateway level (before upstream requests)
4. **Circuit breaker:** 429/5xx errors trigger exponential backoff via Redis state tracking
5. **Plugin sandboxing:** Dynamic plugins run in isolated `ClassLoader` instances; no Spring context access

---

## 10. Troubleshooting

### Container fails to start
```bash
docker-compose logs <service-name>
# e.g.
docker-compose logs analytics
docker-compose logs gateway
```

### `ENCRYPTION_MASTER_KEY` error on analytics startup
Ensure the key is exactly 64 hex characters (32 bytes):
```bash
openssl rand -hex 32  # generates a valid key
```
Update `.env` with the generated key.

### Redis connection refused in gateway
Check `REDIS_URL` is set to `redis:6379` (not `localhost:6379`) inside Docker:
```bash
docker-compose exec gateway env | grep REDIS_URL
```

### Hibernate fails to create tables
Check PostgreSQL is healthy:
```bash
docker-compose logs postgres-db
```
Ensure `SPRING_DATASOURCE_URL` hostname matches the Docker service name (`postgres-db`).

### `Bearer test` returns 401 (Unauthorized)
The `test` bypass only works when Redis is unreachable (no `tenant:cfg:test` key found). To create a real test tenant:
```bash
curl -X POST "http://localhost:8081/api/admin/tenants?name=test-org"
# Copy the returned cognigateApiKey and use it in gateway requests
```

### Plugin compilation error
Read the error from the `/api/admin/plugins/upload` response body. Common issues:
- Class not implementing `AiProviderHandler`
- Using Java features unsupported by Janino (complex lambdas, records)
- Wrong `className` parameter not matching the actual class name in source

---

## 11. Repository Settings (GitHub)

Copy these when setting up the GitHub repository:

**Description:**
> The Zero-Downtime Cognitive Router for Enterprise AI. Self-hosted multi-tenant AI gateway with circuit-breaking, AES-256 key vaulting, and hot-swap plugin compilation.

**Topics:**
```
ai, llm, openai, gateway, java, go, spring-boot, redis, postgresql, docker, enterprise, multi-tenant, openrouter, litellm, self-hosted, api-gateway, circuit-breaker, encryption
```

**Website:** `https://life-experimentalist.github.io/CogniGate`

---

## 12. Development Commands

```bash
# Start full stack
./setup.sh --dev --detach        # Linux/macOS
.\setup.ps1 -Mode dev -Detach    # Windows

# Run Java tests
cd analytics && ./mvnw test

# Run Go tests
cd gateway && go test -v ./...

# Build docs site locally
cd docs && npm run dev

# Tear down
docker-compose down

# Wipe all data and restart fresh
./setup.sh --clean --dev --detach
```

---

## 13. Extending CogniGate

### Add a New LLM Provider (Tier 1 — JSON)
No code required. POST a JSON config to the admin API:
```json
{"baseUrl": "https://api.myprovider.com/v1", "authHeaderFormat": "Bearer %s"}
```

### Add a New LLM Provider (Tier 2 — Java)
1. Write a class implementing `AiProviderHandler`
2. POST to `/api/admin/plugins/upload?className=com.cognigate.plugin.MyHandler`
3. The plugin is immediately available — no restart required

### Add a New Tenant
```bash
curl -X POST "http://localhost:8081/api/admin/tenants?name=new-org"
# Returns cognigateApiKey — distribute to tenant users
```

### Add Provider Keys for a Tenant
```bash
curl -X POST http://localhost:8081/api/admin/tenants/{id}/keys \
  -d '{"providerName":"openai","apiKey":"sk-..."}'
```

### Configure Routing Rules
```bash
curl -X POST http://localhost:8081/api/admin/tenants/{id}/rules \
  -d '{"modelName":"gpt-4","backupModelName":"claude-3","priority":1}'
```
