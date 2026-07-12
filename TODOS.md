# CogniGate — Phase 1 Autonomous Execution Ledger
**Current Status:** In Progress  
**Target Architecture:** Go 1.26 (Edge Proxy), Java 25 LTS / Spring Boot 4.1 (Domain Engine), PostgreSQL 16, Redis 7  
**Root Workspace:** `v:\Code\ProjectCode\CogniGate`

---

## Phase 1: Foundation Scaffolding & Core Architecture

### 1. Root Orchestration & Config
- [ ] **Create `docker-compose.yml`**
  - Configure `cognigate-go` (Port 8080 -> 8080)
  - Configure `cognigate-java` (Port 8081 -> 8081)
  - Configure `postgres-db` (Image: `postgres:16-alpine`, Port 5432, Volume: `pgdata:/var/lib/postgresql/data`)
  - Configure `redis` (Image: `redis:7-alpine`, Port 6379)
  - Set up shared bridge network: `cogni-net`
- [ ] **Create `openapi.yaml`**
  - Define OpenAPI 3.1.0 specification for `POST /v1/chat/completions`
  - Set up Bearer Auth security definitions

### 2. High-Speed Edge Proxy (`/gateway-go`)
- [ ] **Initialize Module:** Create `go.mod` targeting Go 1.26 with dependencies:
  - `github.com/gofiber/fiber/v2`
  - `github.com/go-redis/redis/v8`
- [ ] **Implement `redis.go`:**
  - Build connection pool to Redis using `REDIS_URL` environment variable.
  - Implement background Goroutine listening to `cognigate:cache:invalidate` Pub/Sub channel.
- [ ] **Implement `router.go`:**
  - Define `ChatRequest` and `RoutingConfig` structs matching OpenAI format.
  - Implement round-robin key rotation and exponential backoff state checks against Redis.
  - Add circuit breaker fallback logic to cascade to `BackupModel` when primary keys fail.
- [ ] **Implement `telemetry.go`:**
  - Create non-blocking Goroutine dispatcher to send token usage metrics to the Java domain engine.
- [ ] **Implement `main.go`:**
  - Initialize Fiber app, attach Redis connection, and register `POST /v1/chat/completions`.
- [ ] **Create `Dockerfile`:**
  - Multi-stage build: Stage 1 (`golang:1.26-alpine`) to compile static binary (`CGO_ENABLED=0`), Stage 2 (`alpine:latest` or `scratch`) for minimal runtime image.

### 3. Enterprise Domain & Analytics Engine (`/analytics-java`)
- [ ] **Initialize Build:** Create `pom.xml` targeting Java 25 and Spring Boot 4.1.0 with dependencies:
  - `spring-boot-starter-web`
  - `spring-boot-starter-data-jpa`
  - `org.postgresql:postgresql`
  - `org.codehaus.janino:janino:3.1.12` (Dynamic bytecode compiler)
  - `org.projectlombok:lombok`
- [ ] **Configure `src/main/resources/application.yml`:**
  - Set database JDBC connection URL, username, and password.
  - Enable Project Loom Virtual Threads explicitly: `spring.threads.virtual.enabled: true`.
  - Set Hibernate DDL auto-mode to `update`.
- [ ] **Implement Application Bootstrap:**
  - Create `src/main/java/com/cognigate/CognigateApplication.java`.
- [ ] **Implement Domain Entities (`src/main/java/com/cognigate/entity/`):**
  - `Tenant.java`: Organization domain model and master `cognigateApiKey`.
  - `ProviderKey.java`: Stores provider name and AES-256 encrypted API keys, linked to Tenant.
  - `RoutingRule.java`: Stores priority-indexed model routing and failover definitions.
  - `UsageMetric.java`: Stores audit logs for token consumption and billing attribution.
- [ ] **Implement Cryptographic Vault (`src/main/java/com/cognigate/service/`):**
  - `EncryptionService.java`: Implement AES-256-GCM encryption/decryption for third-party API keys using `ENCRYPTION_MASTER_KEY` env var.
- [ ] **Create `Dockerfile`:**
  - Multi-stage build: Stage 1 (`maven:3.9-eclipse-temurin-25`) to run `mvn clean package -DskipTests`, Stage 2 (`eclipse-temurin:25-jre-jammy`) to run the Fat-JAR.

### 4. Verification & Testing
- [ ] **Execute Multi-Container Build:** Run `docker-compose up --build -d`.
- [ ] **Verify Database Auto-DDL:** Confirm PostgreSQL logs show Hibernate successfully generating `tenant`, `provider_key`, `routing_rule`, and `usage_metric` tables.
- [ ] **Test Edge Routing:** Issue test `curl` payload to `http://localhost:8080/v1/chat/completions` verifying Fiber proxy interception.
