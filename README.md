<div align="center">

<h1>
  <img src="https://raw.githubusercontent.com/Life-Experimentalist/CogniGate/main/docs/public/logo.svg" alt="CogniGate Logo" width="64" height="64" /><br/>
  CogniGate
</h1>

<p><strong>The Zero-Downtime Cognitive Router for Enterprise AI</strong></p>

<p>
  <em>Self-hosted · Multi-tenant · OpenAI-compatible · Open Source</em>
</p>

<p>
  <a href="https://github.com/Life-Experimentalist/CogniGate/actions/workflows/ci.yml">
    <img src="https://github.com/Life-Experimentalist/CogniGate/actions/workflows/ci.yml/badge.svg" alt="CI Status" />
  </a>
  <a href="https://github.com/Life-Experimentalist/CogniGate/releases">
    <img src="https://img.shields.io/github/v/release/Life-Experimentalist/CogniGate?include_prereleases&style=flat-square" alt="Latest Release" />
  </a>
  <a href="LICENSE">
    <img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg?style=flat-square" alt="Apache 2.0 License" />
  </a>
  <a href="https://github.com/Life-Experimentalist/CogniGate/stargazers">
    <img src="https://img.shields.io/github/stars/Life-Experimentalist/CogniGate?style=flat-square" alt="GitHub Stars" />
  </a>
</p>

<p>
  <a href="https://life-experimentalist.github.io/CogniGate">📖 Documentation</a> ·
  <a href="https://github.com/Life-Experimentalist/CogniGate/issues/new?template=bug_report.yml">🐛 Report Bug</a> ·
  <a href="https://github.com/Life-Experimentalist/CogniGate/issues/new?template=feature_request.yml">✨ Request Feature</a> ·
  <a href="https://github.com/Life-Experimentalist/CogniGate/discussions">💬 Discussions</a>
</p>

</div>

---

## What Is CogniGate?

CogniGate is a **self-hosted enterprise AI infrastructure platform** that sits between your applications and LLM providers (OpenAI, Anthropic, Groq, Mistral, etc.). It is the open-source alternative to OpenRouter and LiteLLM — built for organizations that need full control over their AI traffic.

### Key Capabilities

| Feature | Description |
|---|---|
| **OpenAI-Compatible API** | Drop-in replacement — zero client code changes |
| **Zero-Downtime Key Rotation** | Redis-backed atomic key cycling with instant cache invalidation |
| **Circuit Breaker & Failover** | Exponential backoff on 429/5xx with automatic cascade to backup models |
| **Multi-Tenant Isolation** | Per-tenant routing rules, API keys, and billing — all fully isolated |
| **AES-256-GCM Key Vault** | All provider API keys encrypted at rest, never stored in plaintext |
| **Hot-Swap Plugin Engine** | Upload `.java` source at runtime — Janino compiles in-memory, zero restart |
| **Enterprise Billing** | Automated monthly invoicing with per-tenant token cost tracking |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         External Clients                         │
│           (OpenAI SDK / curl / LangChain / LlamaIndex)           │
└───────────────────────────────┬─────────────────────────────────┘
                                │ POST /v1/chat/completions
                                │ Authorization: Bearer cg-xxx
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Gateway  (Go 1.26 + Fiber v2)                 │
│                           :8080                                  │
│  ┌──────────────┐  ┌───────────────┐  ┌──────────────────────┐  │
│  │ Auth & Tenant │  │ Key Rotation  │  │  Circuit Breaker     │  │
│  │  Resolution  │  │ (Round-Robin) │  │  (Exp. Backoff)      │  │
│  └──────────────┘  └───────────────┘  └──────────────────────┘  │
│         │                  │                    │                 │
│  ┌──────▼──────────────────▼────────────────────▼─────────────┐  │
│  │              Redis 7 — Fast-Path Cache + Pub/Sub            │  │
│  │         key: tenant:cfg:{cognigateApiKey}                   │  │
│  └─────────────────────────────────────────────────────────────┘  │
└────────────────────┬────────────────────────────────────────────┘
         │ Forward   │  goroutine: POST /api/webhook/telemetry
         ▼           ▼
  LLM Providers   Analytics  (Java 26 + Spring Boot 4.1)
  (OpenAI, etc.)     :8081
                      │
              ┌───────┴────────┐
              │                │
        PostgreSQL 16     Plugin Engine
          :5432          (Janino + ClassLoader)
```

---

## Quick Start

### Prerequisites

- **Docker** & **Docker Compose** (v2+)
- No Java or Go installation required for running — everything is containerized

### One-Command Setup

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/Life-Experimentalist/CogniGate/main/setup.sh | bash
# Or clone first:
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
./setup.sh --dev --detach
```

```powershell
# Windows (PowerShell)
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
.\setup.ps1 -Mode dev -Detach
```

This single command:
1. ✅ Copies `.env.example` → `.env`
2. ✅ Auto-generates a secure `ENCRYPTION_MASTER_KEY`
3. ✅ Builds and starts all 4 services (Gateway, Analytics, PostgreSQL, Redis)

### Verify It's Running

```bash
# Health check
curl http://localhost:8080/health
# → OK

# Create a tenant
curl -X POST "http://localhost:8081/api/admin/tenants?name=my-org"
# → {"id":1,"name":"my-org","cognigateApiKey":"cg-..."}

# Add an OpenAI API key for the tenant
curl -X POST http://localhost:8081/api/admin/tenants/1/keys \
  -H "Content-Type: application/json" \
  -d '{"providerName":"openai","apiKey":"sk-proj-..."}'

# Add a routing rule
curl -X POST http://localhost:8081/api/admin/tenants/1/rules \
  -H "Content-Type: application/json" \
  -d '{"modelName":"gpt-4","backupModelName":"claude-3-opus","priority":1}'

# Make your first AI call through CogniGate
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer cg-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello, CogniGate!"}]}'
```

---

## Configuration

All configuration is documented in:
- **[`.env.example`](.env.example)** — Environment variables reference
- **[`cognigate.config.yml`](cognigate.config.yml)** — Unified configuration manifest

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `REDIS_URL` | `redis:6379` | Redis host:port |
| `SPRING_DATASOURCE_URL` | `jdbc:postgresql://postgres-db:5432/cognigate` | PostgreSQL JDBC URL |
| `SPRING_DATASOURCE_USERNAME` | `cognigate_user` | DB username |
| `SPRING_DATASOURCE_PASSWORD` | `cognigate_pass` | DB password |
| `ENCRYPTION_MASTER_KEY` | *(auto-generated)* | 64-char hex AES-256 master key |
| `POSTGRES_DB` | `cognigate` | Database name |

---

## Documentation

Full documentation is available at **[https://life-experimentalist.github.io/CogniGate](https://life-experimentalist.github.io/CogniGate)**.

| Section | Description |
|---|---|
| [Getting Started](https://life-experimentalist.github.io/CogniGate/docs/getting-started) | Installation, first run, quick test |
| [Architecture](https://life-experimentalist.github.io/CogniGate/docs/architecture) | System design, data flows, component interactions |
| [API Reference](https://life-experimentalist.github.io/CogniGate/docs/api) | All endpoints, request/response schemas |
| [Plugin Development](https://life-experimentalist.github.io/CogniGate/docs/plugins) | Building custom providers |
| [Security](https://life-experimentalist.github.io/CogniGate/docs/security) | Encryption, auth, tenant isolation |
| [Billing](https://life-experimentalist.github.io/CogniGate/docs/billing) | Token tracking, invoicing, cost config |
| [Deployment](https://life-experimentalist.github.io/CogniGate/docs/deployment) | Production hardening, TLS, Kubernetes |

**AI Agent Context:** For AI-assisted troubleshooting and customization, pass the raw URL of [`COGNIGATE_AI_CONTEXT.md`](https://raw.githubusercontent.com/Life-Experimentalist/CogniGate/main/COGNIGATE_AI_CONTEXT.md) to your AI assistant.

---

## Plugin System

CogniGate supports two tiers of custom LLM provider integration:

### Tier 1: JSON Mapper (No-Code)
For OpenAI-compatible providers like Groq, TogetherAI, vLLM:
```bash
curl -X POST http://localhost:8081/api/admin/plugins/upload \
  -F "file=@groq.json" \
  -F "className=json-mapper"
```

### Tier 2: Dynamic Java (Runtime Compilation)
Upload `.java` source — Janino compiles it in memory:
```java
public class BedrockHandler implements AiProviderHandler {
    @Override
    public String handleRequest(String prompt, String apiKey) throws Exception {
        // AWS SigV4 signing + Bedrock API call
        return callBedrock(prompt, apiKey);
    }
}
```
```bash
curl -X POST http://localhost:8081/api/admin/plugins/upload \
  -F "file=@BedrockHandler.java" \
  -F "className=BedrockHandler"
```

---

## Tech Stack

| Component | Technology |
|---|---|
| Edge Proxy | Go 1.26, Fiber v2, gofiber/fiber |
| Domain Engine | Java 26, Spring Boot 4.1, Hibernate, Project Loom (Virtual Threads) |
| Plugin Compiler | Janino 3.1.12 (in-memory Java compilation) |
| Cache & Pub/Sub | Redis 7 |
| Database | PostgreSQL 16 |
| Encryption | AES-256-GCM (via Java JCA) |
| Container Runtime | Docker, Docker Compose |
| Docs Site | Next.js 15, shadcn/ui, Three.js / React Three Fiber |

---

## Contributing

We welcome contributions! Please read our **[Contributing Guide](.github/CONTRIBUTING.md)** and follow our **[Code of Conduct](.github/CODE_OF_CONDUCT.md)**.

For security vulnerabilities, please see our **[Security Policy](.github/SECURITY.md)** — report privately, never via public issues.

---

## License

Copyright 2026 **VKrishna04** and **Life Experimentalist**

Licensed under the **Apache License, Version 2.0**. See [LICENSE](LICENSE) for the full text.

---

<div align="center">
  <sub>Built with ❤️ by <a href="https://github.com/VKrishna04">VKrishna04</a> and <a href="https://github.com/Life-Experimentalist">Life Experimentalist</a></sub>
</div>
