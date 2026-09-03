# =============================================================================
# CogniGate — AI Context Document
# Version: 2.0.0
# Repository: https://github.com/Life-Experimentalist/CogniGate
# License: Apache 2.0 — Copyright 2026 VKrishna04 and Life Experimentalist
# =============================================================================
# PURPOSE: Give an AI assistant an accurate working model of CogniGate — what
#          it is, what it is not, and where the authoritative answer lives.
#          Every route, field and default below was read out of the source.
#          Where the source is the better answer, this file says so rather
#          than restating it.
# RAW URL: https://raw.githubusercontent.com/Life-Experimentalist/CogniGate/main/COGNIGATE_AI_CONTEXT.md
# =============================================================================

## 1. What CogniGate is

A self-hosted, multi-tenant LLM gateway. Applications hold one CogniGate key and
speak the OpenAI API; provider credentials stay inside the deployment. It is an
open-source alternative to OpenRouter and LiteLLM.

Two processes and a database:

| Process | Stack | Port | Owns |
|---|---|---|---|
| `gateway/` | Go 1.26, Fiber v2 | 8080 | The request path. All configuration, in memory. |
| `analytics/` | Java 25 LTS, Spring Boot 4.1 | 8081 | Durable usage records. Nothing on the request path. |
| `postgres-db` | PostgreSQL 16 | 5432 | One table, `usage_metric`. |

`docs/` is a Next.js 16 site published to GitHub Pages. It is documentation only
and no runtime component depends on it.

## 2. What CogniGate is not

State these plainly if a user asks — earlier drafts of this document and of the
README claimed all of them, and they were never built:

- **No Redis.** Three containers, not four. Configuration is per-process memory.
- **No plugin system.** No Janino, no runtime `.java` upload, no
  `/api/admin/plugins/upload`. Adding a native adapter is a code change in
  `gateway/internal/provider/`.
- **No encryption at rest and no `ENCRYPTION_MASTER_KEY`.** Provider keys live
  in gateway memory, are returned by no route, and are lost on restart. The
  protection is that they are never written down, not that they are enciphered.
- **No invoicing.** Usage is metered and stored; no invoice document, ledger, or
  route that reads one back exists.
- **No Kubernetes assets.** No `deploy/`, `k8s/` or `charts/` directory.
- **One provider adapter**, `kind: "openai"`. It covers everything that
  reimplements the OpenAI wire format — Together, Groq, Fireworks, Azure OpenAI,
  OpenRouter, vLLM, Ollama, LM Studio — because only the base URL differs. An
  unrecognised `kind` falls back to it rather than refusing to route. A provider
  with its own protocol (Anthropic's native API, Bedrock) needs a translating
  proxy in front of it.
- **Only `POST /v1/chat/completions` is proxied.** No embeddings, images or
  audio route exists, whatever a model's `transcribe` alias suggests.

## 3. The two planes

Both are on the gateway, `:8080`. The plane is carried by the key prefix, and
using the wrong one is a distinct refusal (`wrong_plane`, 401) rather than a
generic bad-credential error.

- **Data plane** — `cg-` keys, under `/v1`.
- **Admin plane** — `cga-` keys, under `/admin/v1`. Before any tenant exists the
  only admin credential is `GATEWAY_BOOTSTRAP_KEY` from `.env`.

Nothing administrative lives on `:8081`. If a user is calling
`http://localhost:8081/api/admin/...`, that route does not exist and never did.

### Data plane, `/v1`

```
POST /v1/chat/completions      GET /v1/models        GET /v1/models/*
GET  /v1/usage                 GET /v1/usage/breakdown
GET  /v1/health                GET /v1/meta
GET  /healthz                  (unauthenticated liveness)
```

`GET /v1/usage` takes `?window=day|month` — default `day`, and **not** `since` /
`until`; anything else is a 400 with `param: "window"`. It returns `object`,
`window`, the resolved half-open `since` / `until`, the five totals (`requests`,
`prompt_tokens`, `completion_tokens`, `total_tokens`, `cost_usd`), a `state`,
and a `limits` array. `/v1/usage/breakdown` adds
`?group_by=model|provider|key|client_request_id` (default `model`), returns
`data[]` capped at the 200 costliest buckets, and sets `truncated` when it cut.

### Admin plane, `/admin/v1`

```
GET    /meta                             POST   /catalog/refresh      GET  /audit
POST   /admin-keys                       GET    /admin-keys           DELETE /admin-keys/:id
POST   /tenants                          GET    /tenants
GET    /tenants/:t                       PATCH  /tenants/:t           DELETE /tenants/:t

  ... and per tenant, under /tenants/:t —
POST   /keys        GET /keys        DELETE /keys/:id
POST   /providers   GET /providers   PATCH  /providers/:id   DELETE /providers/:id
PUT    /aliases/:name       GET /aliases        DELETE /aliases/:name
PUT    /routing-rules       GET /routing-rules  DELETE /routing-rules/:id
PUT    /quota               GET /quota          DELETE /quota
PUT    /keys/:id/quota      GET /keys/:id/quota DELETE /keys/:id/quota
POST   /cache/flush         GET /captures       GET /events
POST   /webhooks            GET /webhooks       DELETE /webhooks/:id
GET    /usage               GET /usage/breakdown
```

Creating or deleting a tenant needs a `root` admin key; a tenant-scoped key gets
`insufficient_scope` (403). A tenant-scoped key reaching *another* tenant's
resources gets `resource_not_found`, not 403 — a 403 would confirm the tenant
exists.

## 4. Routing

A model name resolves in this order: a routing rule matching it, then an alias,
then a catalogue model id. Aliases (`fast`, `balanced`, `best`, `transcribe` are
created with every tenant) resolve against the live catalogue, so a client
written against `balanced` keeps working as providers ship new models.

A rule's `chain` is an ordered cascade. Within one provider, keys are a pool and
CogniGate rotates through them before moving to the next candidate. A candidate
whose breaker is open is skipped without a call. When every candidate fails the
response is `upstream_exhausted` (502) carrying an `attempts` array naming each
provider, model, failure kind and status.

Cost per request comes from the provider's `/models` response via CogniGate's
own non-standard `input_cost_per_mtok` / `output_cost_per_mtok` fields, which
OpenAI does not publish. When both are zero the cost is reported as 0 rather
than guessed — which also silently disables `cheapest` / `best` ordering and any
spend budget for that provider.

## 5. Analytics

Three routes, all under `/api/v1/usage`, all requiring
`Authorization: Bearer $ANALYTICS_TOKEN`:

| Route | Purpose |
|---|---|
| `POST /api/v1/usage` | Ingest one record. **Not** `/api/webhook/telemetry`. |
| `GET /api/v1/usage/totals` | What the gateway's `/v1/usage` proxies to. |
| `GET /api/v1/usage/breakdown` | Likewise for `/v1/usage/breakdown`. |

Ingest answers **201** when the record is new and **200** when that `request_id`
was already held, so a retry cannot double-count. It answers **400** — never
500 — on a record missing `request_id`, `tenant_id` or `recorded_at`, because
the gateway retries a 5xx and drops a 4xx; answering 500 to a malformed record
would wedge the queue behind it forever.

Delivery is at-least-once and storage is exactly-once. Metering is off the
request path: a full buffer drops usage records, not requests, and logs
`telemetry buffer full; usage records are being dropped`.

**Schema is one table**, `usage_metric`, unique on `request_id`, indexed on
`(tenant_id, recorded_at)` and `(tenant_id, key_prefix, recorded_at)`. There is
no `tenant`, `provider_key` or `routing_rule` table — that configuration lives
in gateway memory and is deliberately not persisted. `tenant_id` is a plain
string with no foreign key.

A scheduled job (`0 0 0 1 * ?`) logs a priced 30-day total per tenant at a
compiled-in flat $0.0015 per 1,000 tokens. **That rate will not agree with the
per-request `cost_usd`** wherever a provider publishes real rates, and its only
output is a log line. Treat it as a worked starting point, not a billing system.

## 6. Configuration

| Variable | Notes |
|---|---|
| `GATEWAY_BOOTSTRAP_KEY` | Admin bootstrap credential, min 16 chars, no default. Generated by setup. |
| `ANALYTICS_TOKEN` | Shared secret between the two processes. Analytics refuses to start without it. Generated by setup. |
| `SPRING_DATASOURCE_URL` / `_USERNAME` / `_PASSWORD` | PostgreSQL connection. |
| `POSTGRES_DB` / `POSTGRES_USER` / `POSTGRES_PASSWORD` | Database container. |

The gateway itself reads `cognigate.config.yml`. Environment beats file, file
beats defaults, and these override individual settings — each is also read
without the `CG_` prefix: `CG_PORT`, `CG_ADMIN_BOOTSTRAP_KEY`,
`CG_ANALYTICS_URL`, `CG_ANALYTICS_TOKEN`, `CG_METRICS_TOKEN`, `CG_LOG_LEVEL`,
`CG_QUOTA_ENFORCEMENT` (`on` | `observe`), `CG_CACHE_ENABLED`.

`cognigate.config.yml` is the authoritative list and is commented. Read it
rather than trusting a remembered key name.

## 7. Behaviours users misread as bugs

- **A restart loses all configuration.** Tenants, keys, providers, aliases,
  rules and quotas are memory-resident. Usage survives; it is in PostgreSQL.
- **Replicas do not share configuration.** An admin call lands on exactly one
  gateway. Running more than one behind a load balancer is not supported by any
  synchronisation mechanism, because there is none.
- **A quota can be crossed by a concurrent burst.** Metering is asynchronous, so
  the cap is enforced on the next request rather than retroactively.
- **`quotas.enforcement: observe` serves over-cap requests** by design, emitting
  events and setting `X-CogniGate-Quota-State`. It is for sizing caps.
- **`upstream_stream_stalled` arrives as a terminal SSE event, never a status.**
  The 200 went out with the first token. A streaming client must read the last
  event rather than trusting the status line.
- **Usage missing while requests succeed** is the expected shape of an analytics
  outage, or a mismatched `ANALYTICS_TOKEN` — analytics answers 401, the gateway
  treats 4xx as permanent and drops the record. Look for
  `usage record could not be persisted`.

Every error response carries a `code`; the code is the diagnosis, not the
status. `X-CogniGate-Request-Id` is on every response including early refusals,
and `X-CogniGate-Quota-State` is `ok`, `soft-exceeded` or `hard-exceeded`.

## 8. Commands

```bash
./setup.sh --dev --detach            # or:  .\setup.ps1 -Mode dev -Detach
docker compose ps                    # three services, all healthy
docker compose logs -f gateway
curl -s http://localhost:8080/healthz

cd gateway    && go test ./... -count=1
cd analytics  && ./mvnw -B test
cd docs       && npm run build

docker compose down                  # stop
docker compose down -v               # stop and wipe the database
```

The `conformance/` suite is a separate Go module that exercises GW-1 … GW-14
against a running stack, one file per requirement. It needs the whole stack up.

## 9. Where the authoritative answer lives

Prefer these over anything remembered, including this file:

| Question | Source |
|---|---|
| What a requirement actually demands | `spec/gw-01…gw-14` |
| Every config key and its default | `cognigate.config.yml` |
| Exact request/response shapes | `gateway/internal/server/*.go`, and `docs/app/docs/api` |
| Error codes and their meanings | `docs/app/docs/troubleshooting` |
| What is proven to work | `conformance/gw*_test.go` |
| Data retention and debug capture | `docs/app/docs/privacy`, `spec/gw-14-privacy.md` |

If this document and the source disagree, the source is right and this document
is stale — say so rather than reconciling them.
