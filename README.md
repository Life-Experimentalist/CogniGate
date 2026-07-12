# CogniGate

> **Branding Tagline**: *The Zero-Downtime Cognitive Router for Enterprise AI.*

CogniGate is a self-hosted, multi-tenant B2B AI infrastructure platform designed to serve as an enterprise-grade, private alternative to OpenRouter and LiteLLM.

It adopts a **polyglot architecture**:
- **/gateway**: A high-speed Go service powered by Fiber v2 and Go-Redis. It serves high-frequency chat prompt routing with circuit breakers, rate limits, and key rotation.
- **/analytics**: A robust Java Spring Boot 4.1 engine leveraging Java 25 Virtual Threads (Project Loom) to manage corporate tenants, encryption of provider API keys, billing, and dynamic bytecode hot-swapping.

---

## Technical Features

### 1. High-Speed Edge Proxy (`/gateway`)
* Go 1.26 runtime utilizing Go-Redis v8 and Gofiber v2.
* Redis Pub/Sub invalidation channels for instant config synchronization.
* Non-blocking asynchronous telemetry dispatch to log metrics.

### 2. Domain & Analytics Engine (`/analytics`)
* Java 25 & Spring Boot 4.1.0 with Spring Data JPA, Security, Web, and Redis.
* Dynamic bytecode hot-swapping utilizing the **Janino** memory compiler inside isolated `ClassLoader` instances to prevent Metaspace leaks.
* Cryptographic Key Vault utilizing **AES-256-GCM** to secure external provider keys.
* Scheduled monthly tiered invoice calculation engine.

---

## Project Structure

```tree
CogniGate/
├── gateway/              # Go-based Edge Proxy service
│   ├── main.go           # Fiber route registration and middleware pipeline
│   ├── router.go         # OpenAI-compliant proxy routing and mock fallback
│   ├── redis.go          # Connection pool and Cache invalidation Pub/Sub listener
│   ├── telemetry.go      # Non-blocking async metrics dispatcher
│   ├── go.mod            # Go module dependencies
│   └── Dockerfile        # Multi-stage static binary compiler
├── analytics/            # Java Spring Boot Domain Engine
│   ├── pom.xml           # Maven descriptor with Loom, Janino, and Security
│   ├── src/main/
│   │   ├── resources/
│   │   │   └── application.yml   # Database config, Hibernate DDL, and Loom toggles
│   │   └── java/com/cognigate/
│   │       ├── CognigateApplication.java # Bootstrap class
│   │       ├── config/
│   │       │   ├── SecurityConfig.java   # JWT/API Key auth & CORS policies
│   │       │   └── ThreadConfig.java     # Tomcat virtual thread customization
│   │       ├── controller/
│   │       │   ├── AdminController.java  # Admin UI endpoints & key vault uploads
│   │       │   └── WebhookController.java # Ingestion point for proxy telemetry
│   │       ├── entity/
│   │       │   ├── Tenant.java
│   │       │   ├── ProviderKey.java
│   │       │   ├── RoutingRule.java
│   │       │   └── UsageMetric.java
│   │       ├── repository/
│   │       │   └── ... (JPA repositories)
│   │       ├── service/
│   │       │   ├── BillingService.java
│   │       │   ├── EncryptionService.java
│   │       │   └── CacheSyncService.java
│   │       └── plugin/
│   │           ├── AiProviderHandler.java
│   │           ├── PluginManager.java
│   │           ├── JsonMapper.java
│   │           └── AnthropicHandler.java
│   └── Dockerfile        # Multi-stage Maven JRE build
├── docker-compose.yml    # Main orchestration connecting Go, Java, Postgres, and Redis
├── openapi.yaml          # Spec for /v1/chat/completions
├── postman_collection.json # E2E tests
├── ai_agent_instructions.md # Rules for LLM plugin compilation
└── README.md             # This guide
```

---

## Operational Runbook

### 1. Build and Run via Docker Compose
To build and run all services (Go proxy, Java backend, PostgreSQL, and Redis) in a single bridge network:
```bash
docker-compose up --build -d
```

### 2. Verify Database Creation
Confirm PostgreSQL is initialized and that Hibernate auto-generates tables:
```bash
docker-compose logs postgres-db
docker-compose logs cognigate-java
```

### 3. Verify Edge Proxy Routing
Issue a test curl command to check if the edge proxy intercepts the request successfully:
```bash
curl -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello CogniGate!"}]}'
```
You should receive a `200 OK` response with a JSON payload mocking the completions.
