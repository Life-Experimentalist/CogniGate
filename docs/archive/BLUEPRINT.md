# Project Blueprint: CogniGate

> **Superseded — kept for the record.** This is the original design document,
> written before any code existed. Parts of it describe things that were never
> built: the hot-swap plugin engine, the Redis cache layer, and the file tree
> it sketches. It is preserved because it is the record of what was intended,
> not a description of what ships. For what the gateway actually does, read the
> README and `docs/`; for what was dropped and why, the CHANGELOG's *Removed*
> section.

**Official Repository & Directory Name:** `CogniGate`

> **Branding Tagline:** _The Zero-Downtime Cognitive Router for Enterprise AI._

---

## 1. Executive Summary & Core Objective

**CogniGate** is a self-hosted, multi-tenant B2B AI infrastructure platform engineered to serve as an enterprise-grade, private alternative to OpenRouter and LiteLLM.

By strictly adhering to a **polyglot architecture**, CogniGate eliminates the traditional trade-offs between network throughput and complex domain modeling:

- **The Edge Proxy (`gateway-go`):** Compiled to a lightweight Go binary, it handles high-frequency prompt routing, token-bucket rate limiting, stateful round-robin key rotation, and circuit-breaking fallbacks with sub-millisecond overhead.
- **The Domain Engine (`analytics-java`):** Built on Spring Boot and Java Virtual Threads, it manages multi-tenant isolation, tiered corporate billing, AES-256 zero-trust key encryption, and real-time in-memory bytecode compilation for proprietary AI model integrations.

### Why Just "CogniGate"?

Keeping the name strictly to **CogniGate** (and the repo as `/cognigate`) avoids naming fatigue, clean-links cleanly in environment variables (`COGNIGATE_API_KEY`), and prevents directory path bloating during containerized deployments.

---

## 2. Technical Stack & Runtime Standards

### High-Speed Edge Proxy (`/gateway-go`)

- **Runtime / Language:** Go 1.26 (Statically linked standalone binary).
- **HTTP Framework:** Fiber v2 (`github.com/gofiber/fiber/v2`) — chosen for zero-memory allocation routing and raw epoll/kqueue performance.
- **State & Caching:** Go-Redis v8 (`github.com/go-redis/redis/v8`) — handles distributed token buckets and failover state tracking.
- **Concurrency Model:** Goroutine-driven asynchronous telemetry dispatch (fire-and-forget usage reporting to the Java engine).

### Enterprise Domain & Analytics Engine (`/analytics-java`)

- **Runtime / Language:** Java 21 / Java 25 LTS (Compiled to an Eclipse Temurin Fat-JAR with Project Loom / Virtual Threads explicitly enabled via `spring.threads.virtual.enabled=true`).
- **Framework:** Spring Boot 4.1 (Spring Web, Spring Data JPA, Spring Security, Spring Actuator).
- **Database ORM:** Hibernate / JPA with the PostgreSQL JDBC Driver.
- **Dynamic Compiler:** Janino 3.1.12 (`org.codehaus.janino:janino`) — enables real-time in-memory compilation of `.java` plugin files without JVM restarts.
- **Serialization:** Jackson (JSON/TOML mapping) + Lombok (boilerplate reduction).

### Shared Infrastructure & Persistence

- **Relational Database:** PostgreSQL 16 Alpine — single source of truth for organizations, encrypted provider keys, routing failover rules, and financial billing ledgers.
- **Distributed Cache & Pub/Sub:** Redis 7 Alpine — acts as the fast-path configuration bridge between Java and Go, and broadcasts instant cache-invalidation events across nodes.
- **Container Orchestration:** Docker & Docker Compose (Multi-stage builds producing sub-30MB Go images and optimized Java runtime containers).

---

## 3. Comprehensive Monorepo File Architecture

```tree
cognigate/
├── .github/
│   └── workflows/
│       └── ci-cd.yml                     # Multi-stage automated builds pushing to GHCR
├── gateway-go/                           # High-throughput Go Edge Proxy Service
│   ├── main.go                           # Fiber bootstrap, middleware pipeline, and route registration
│   ├── router.go                         # Unified OpenAI-format handler, circuit breaker, and failover cascade
│   ├── redis.go                          # Redis connection pool, Pub/Sub listener, and fast-path reader
│   ├── telemetry.go                      # Non-blocking goroutine dispatcher for token usage reporting
│   ├── go.mod                            # Go module dependencies
│   ├── go.sum                            # Dependency checksums
│   └── Dockerfile                        # Multi-stage static binary build (scratch/alpine target)
├── analytics-java/                       # Enterprise Java Spring Boot Backend
│   ├── pom.xml                           # Maven dependencies, Java 21 target, Janino compiler config
│   ├── Dockerfile                        # Multi-stage Maven build to Eclipse Temurin JRE Fat-JAR
│   └── src/main/
│       ├── resources/
│       │   └── application.yml           # DB configs, Virtual Thread toggles, and JPA auto-ddl rules
│       └── java/com/cognigate/
│           ├── CognigateApplication.java # Spring Boot application bootstrap
│           ├── config/
│           │   ├── SecurityConfig.java   # Stateless JWT/API-key authentication and CORS policies
│           │   └── ThreadConfig.java     # Tomcat virtual thread executor overrides
│           ├── controller/
│           │   ├── AdminController.java  # Tenant UI endpoints, key vault, and plugin upload handlers
│           │   └── WebhookController.java # Ingests async usage telemetry from the Go proxy
│           ├── entity/
│           │   ├── Tenant.java           # Organization domain model and master CogniGate API key
│           │   ├── ProviderKey.java      # AES-256 encrypted third-party API credentials
│           │   ├── RoutingRule.java      # Priority-indexed failover routing definitions
│           │   └── UsageMetric.java      # High-volume audit logs for token consumption and billing
│           ├── repository/
│           │   ├── TenantRepository.java # JPA interface for tenant queries
│           │   ├── ProviderKeyRepo.java  # JPA interface for encrypted credentials
│           │   ├── RoutingRuleRepo.java  # JPA interface for failover priority orders
│           │   └── UsageMetricRepo.java  # JPA interface for analytics logging
│           ├── service/
│           │   ├── BillingService.java   # Scheduled monthly tiered invoice calculation engine
│           │   ├── EncryptionService.java# AES-256-GCM encryption/decryption for API key vaulting
│           │   └── CacheSyncService.java # Syncs PostgreSQL routing rules to Redis & publishes invalidation events
│           └── plugin/
│               ├── AiProviderHandler.java# Interface contract for all LLM provider plugins
│               ├── PluginManager.java    # Janino in-memory compiler and isolated ClassLoader manager
│               ├── JsonMapper.java       # Handler for standard JSON/TOML-configurable endpoints
│               └── AnthropicHandler.java # Native reference implementation for Anthropic Claude
├── docker-compose.yml                    # Local orchestration linking Go, Java, Postgres, and Redis
├── openapi.yaml                          # OpenAPI 3.1.0 specification for `/v1/chat/completions`
├── postman_collection.json               # E2E testing suite (Proxy requests, upload plugin, billing test)
├── ai_agent_instructions.md              # System prompt and strict rules for AI agents generating plugins
└── README.md                             # Architectural overview and operational runbook

```

---

## 4. Deep-Dive: Core System Mechanics

### Mechanic A: Fast-Path Routing & Pub/Sub Cache Invalidation

To prevent the Go proxy from bottlenecking on PostgreSQL queries, **all routing rules and decrypted keys are cached in Redis**.

1. When a tenant makes a request to `/v1/chat/completions`, `gateway-go` reads the tenant's profile directly from Redis (`GET tenant:cfg:{api_key}`).
2. When an administrator updates a key or routing rule via the Java Admin Board, `CacheSyncService` updates PostgreSQL, writes the new configuration to Redis, and publishes an event to the Redis Pub/Sub channel `cognigate:cache:invalidate`.
3. The Go proxy listens to this channel and instantly drops its internal L1 memory cache for that tenant, guaranteeing zero-latency updates without database polling.

### Mechanic B: The Resilience State Machine

When `gateway-go` processes a prompt, it executes a strict failure-mitigation loop:

```text
[Incoming Request] ──> [Select Primary Model] ──> [Filter Active Keys (not in backoff)]
                                                           │
                                                           ▼
[Return Response] <── (Success 200) <── [Execute Request via Round-Robin Key]
                                                           │
                                             (Failure 429 / 5xx / Timeout)
                                                           │
                                                           ▼
                                         [Set Redis Backoff: 2^failures mins]
                                                           │
                                                           ▼
                                           [Are more primary keys left?]
                                            ├── YES ──> [Try Next Key]
                                            └── NO  ──> [Cascade to Backup Model]

```

### Mechanic C: Hybrid Plugin Engine (JSON vs. In-Memory Java)

CogniGate supports two tiers of custom provider onboarding:

- **Tier 1: Declarative JSON/TOML Mapping (`JsonMapper.java`):** For endpoints that conform strictly to the OpenAI chat completion schema (e.g., Groq, TogetherAI, local vLLM). The admin uploads a simple JSON file specifying the target Base URL and header authorization format.
- **Tier 2: Dynamic Bytecode Hot-Swapping (`PluginManager.java`):** For providers with proprietary streaming protocols or complex cryptographic signing requirements (e.g., AWS Bedrock SigV4). The admin uploads a raw `.java` file implementing `AiProviderHandler`. Janino compiles the source code into bytecode in memory and injects it into the Spring ApplicationContext with zero downtime.

---

## 5. Architectural FAQs & Edge-Case Mitigations (Plugging the Holes)

### How does CogniGate handle a complete Redis cluster outage?

If Redis becomes unreachable, `gateway-go` trips an internal circuit breaker and falls back to a **read-only local memory cache** (TTL 5 minutes) populated during previous successful requests. While new routing rule changes won't reflect until Redis recovers, active API routing and failover mechanics continue uninterrupted.

### Doesn't hot-swapping Java classes at runtime cause Metaspace memory leaks?

Yes, standard classloading without cleanup will eventually exhaust JVM Metaspace if plugins are repeatedly recompiled. To prevent this, `PluginManager.java` loads each dynamic class inside a **temporary, isolated child `ClassLoader**`. When a plugin is updated, the reference to the old `ClassLoader` is severed, allowing the JVM garbage collector to completely sweep the old bytecode from Metaspace.

### How is token usage calculated if a fallback model uses a different tokenizer than the primary model?

CogniGate does not calculate tokens locally at the edge. Instead, `gateway-go` extracts the standardized `usage` block (`prompt_tokens`, `completion_tokens`, `total_tokens`) returned directly in the JSON response payload of the AI provider. If a non-standard provider fails to return a usage block, `gateway-go` applies a fallback mathematical heuristic (~4 characters per token for English text) before dispatching the telemetry payload to the Java billing engine.

### How do we prevent "noisy neighbor" problems where one tenant exhausts system connections?

While upstream API limits are handled via round-robin rotation, local proxy exhaustion is prevented by **Bucket4j / Go-Redis rate limiting**. Each tenant API key is assigned a strict maximum requests-per-second (RPS) and concurrent connection limit. If a tenant exceeds their local threshold, `gateway-go` rejects the request immediately with `429 Too Many Requests` _before_ initiating any upstream HTTP connections or hitting third-party APIs.

---

## 6. Immediate Phase 1 Execution Roadmap (For Autonomous Agent)

Pipe these exact instructions into your local agent (`gstack` / Antigravity) to initiate Phase 1 construction:

1. **Initialize Workspace:** Create the root directory `cognigate/` and generate the exact subdirectory structure and empty placeholder files outlined in Section 3.
2. **Orchestration Scaffold:** Write `docker-compose.yml` configuring networking between `gateway-go` (port 8080), `analytics-java` (port 8081), `postgres-db` (port 5432), and `redis` (port 6379). Include health checks and persistent volume mapping for PostgreSQL (`pgdata`).
3. **Domain & ORM Foundation (Java):**

- Configure `analytics-java/pom.xml` with Spring Boot 4.1, Java 21 compiler properties, PostgreSQL JDBC driver, Spring Data JPA, Lombok, and Janino 3.1.12.
- Implement the four JPA entity classes (`Tenant`, `ProviderKey`, `RoutingRule`, `UsageMetric`) with strict relational mapping (`@OneToMany`, `@ManyToOne`, `@Column(unique=true)`).
- Implement `EncryptionService.java` utilizing `javax.crypto.Cipher` with AES-256-GCM to securely encrypt and decrypt the `apiKey` string attribute in `ProviderKey`.

4. **Edge Proxy Skeleton (Go):**

- Initialize `gateway-go/go.mod` with Go 1.26, `gofiber/fiber/v2`, and `go-redis/redis/v8`.
- Write `redis.go` to establish the Redis client connection pool and a background Goroutine listening to the `cognigate:cache:invalidate` Pub/Sub channel.
- Write `main.go` and `router.go` to expose `POST /v1/chat/completions`, validate incoming Bearer tokens against Redis, and return a mock OpenAPI-compliant JSON response.

5. **Verification Build:** Execute `docker-compose up --build -d` and verify that all four containers initialize cleanly, database tables are auto-generated by Hibernate, and `curl -i http://localhost:8080/v1/chat/completions -H "Authorization: Bearer test"` successfully hits the Go edge proxy.
