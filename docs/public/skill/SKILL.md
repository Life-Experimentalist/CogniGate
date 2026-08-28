---
name: cognigate
description: Use when installing, operating, or wiring an application into CogniGate — the self-hosted OpenAI-compatible LLM gateway. Covers first install, tenant and key provisioning, provider registration, routing aliases, quotas, health checks, and the integration path for any codebase that already calls an LLM API.
---

# CogniGate

CogniGate is a self-hosted gateway that sits in front of every model an
organisation uses. Applications hold one CogniGate key and speak the OpenAI
Chat Completions API; provider credentials stay inside the deployment and are
never handed to a caller.

This skill is enough to install it, provision it, wire an application into it,
and diagnose it, without reading the source.

**Source of truth:** <https://github.com/Life-Experimentalist/CogniGate>.
If anything here disagrees with the running gateway, the gateway wins — ask it:
`GET /v1/meta` reports the version and `GET /admin/v1/meta` reports the
control-plane surface.

---

## Non-negotiable rules

1. **Never print, log, echo or commit a provider key or a minted `cg-` secret.**
   Write them into `.env` or a secret store and refer to them by variable name
   thereafter. `.env` is gitignored in the CogniGate repo; make sure it is
   gitignored in whatever repo you put one in.
2. **The minted secret is shown exactly once.** `POST .../keys` answers with
   `{"key": {...}, "secret": "cg-...", "warning": "..."}`. The usable
   credential is `.secret`, **not** `.key` — `.key` is metadata and the store
   keeps only a hash. If you drop it, the key is unrecoverable: revoke it and
   mint another.
3. **Never point an application at a provider directly "just to unblock it".**
   That is the failure mode CogniGate exists to prevent; it puts a provider
   credential back into an application process. Fix the gateway instead.
4. **Do not invent configuration.** Every field below is one the gateway
   actually parses. Guessing produces a 400 that reads like a gateway bug.
5. **Ask before destroying data.** `./setup.sh --clean` removes the Postgres
   volume — every tenant, key, provider and usage record with it. It is never
   the right first response to a failing container.

---

## Install

Prerequisites: Docker and Docker Compose v2. Nothing else — no Go, no JDK.

```bash
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
./setup.sh --dev --detach
```

Windows PowerShell:

```powershell
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
.\setup.ps1 -Mode dev -Detach
```

`setup.sh` flags: `--dev` (default) or `--prod`, plus `--detach`, `--clean`,
`--help`.

Three containers come up, and only three. If you are looking for a fourth,
there isn't one:

| Container | What it is | Port |
| --- | --- | --- |
| `gateway` | Go / Fiber edge proxy — the whole API surface | 8080 |
| `analytics` | Java / Spring Boot usage metering | 8081 |
| `postgres-db` | PostgreSQL 16 — the only durable store | 5432 |

**Verify before doing anything else.** Do not proceed on "the script printed
success":

```bash
docker compose ps
curl -fsS http://localhost:8080/healthz
```

`/healthz` is unauthenticated, and is the only endpoint that is.

### Generated secrets

Setup writes a `.env` from `.env.example` and fills in the two credentials that
have no safe default:

- `GATEWAY_BOOTSTRAP_KEY` — the root admin credential, minimum 16 characters.
  It is the only key that exists before any tenant does. Generate with
  `openssl rand -hex 24`.
- `ANALYTICS_TOKEN` — the shared secret between gateway and analytics.
  `openssl rand -hex 32`. If the two sides disagree the analytics service
  answers 401, the gateway treats a 4xx as permanent and drops the record after
  one attempt, and metering fails quietly. Grep the gateway log for
  `usage record could not be persisted`.

If either is still `replace_me`, the service refuses to start. That is
deliberate, not a bug — an unedited example file must not become a deployment
whose admin credential is published on the internet.

---

## Provision

Everything below is the admin plane: `/admin/v1/**`, authenticated with the
bootstrap key. Export it once, from the `.env` — never inline it in a command
you are about to show someone.

```bash
export CG_ADMIN="$(grep -E '^GATEWAY_BOOTSTRAP_KEY=' .env | cut -d= -f2-)"
export CG=http://localhost:8080
```

**1 — Create a tenant.** A tenant is the isolation boundary: keys, providers,
aliases, quotas and usage all belong to exactly one.

```bash
curl -sS -X POST $CG/admin/v1/tenants -H "Authorization: Bearer $CG_ADMIN" -H 'Content-Type: application/json' -d '{"name":"my-org"}'
```

Returns `201` with `{"id":"ten_...","name":"my-org","status":"active",...}`.
Keep the id — every route below is scoped under it. `export TENANT=ten_...`

**2 — Register a provider.** This is where the real credential goes, and the
last place it appears.

```bash
curl -sS -X POST $CG/admin/v1/tenants/$TENANT/providers -H "Authorization: Bearer $CG_ADMIN" -H 'Content-Type: application/json' -d '{"name":"openai","base_url":"https://api.openai.com/v1","keys":["THE_PROVIDER_KEY"]}'
```

| Field | Meaning |
| --- | --- |
| `name` | Your label for it. Appears in `X-CogniGate-Served-By`. |
| `base_url` | Required. A trailing slash is stripped. |
| `keys` | One or more; at least one is required. Several rotate. |
| `kind` | Optional, defaults to `openai`. |
| `enabled` | Optional, defaults to `true`. |

There is one adapter, `openai`, and it is enough for every OpenAI-compatible
endpoint: Together, Groq, Fireworks, Azure OpenAI, OpenRouter, vLLM, Ollama,
LM Studio. An unrecognised `kind` falls back to it. Registering a second
provider is how failover gets somewhere to fail over to.

**3 — Mint the key the application will hold.**

```bash
curl -sS -X POST $CG/admin/v1/tenants/$TENANT/keys -H "Authorization: Bearer $CG_ADMIN" -H 'Content-Type: application/json' -d '{"name":"checkout-service"}'
```

Read `.secret` from the response — see rule 2. Optional fields: `plane`
(`data`, the default, or `admin`) and `expires_at` (RFC 3339, must be in the
future). Name each key after the thing that holds it, so that revoking one is a
decision somebody can make without archaeology.

**4 — Route.** Every new tenant is seeded with four portable aliases, so there
is something to call before anyone has configured anything:

`fast` (cheapest chat) · `balanced` · `best` · `transcribe`

```bash
curl -sS -X POST $CG/v1/chat/completions -H "Authorization: Bearer $CG_KEY" -H 'Content-Type: application/json' -d '{"model":"fast","messages":[{"role":"user","content":"ping"}]}'
```

The response carries `X-CogniGate-Served-By: <provider>/<model>` — that header
is how you find out what an alias actually resolved to. Read it whenever
routing surprises you.

---

## The rest of the control plane

Tenant-scoped, all under `/admin/v1/tenants/:tenant`:

| Route | What it does |
| --- | --- |
| `PUT /aliases/:name` | Define an alias. Body: `pin`, `capabilities[]`, `min_context_window`, `provider_preference[]`, `cost_tier` (`cheapest`, `balanced`, `best`). An alias that collides with a real model id is refused. |
| `GET /aliases`, `DELETE /aliases/:name` | List, remove. |
| `PUT /routing-rules`, `GET`, `DELETE /routing-rules/:id` | Fallback chains and ordering. |
| `PUT /quota`, `GET /quota`, `DELETE /quota` | Per-tenant ceilings. |
| `PUT /keys/:id/quota` and friends | The same, narrowed to one key. |
| `GET /usage`, `GET /usage/breakdown` | Metered consumption. |
| `GET /events`, `GET /captures` | What happened, and captured requests for debugging. |
| `POST /webhooks`, `GET`, `DELETE /webhooks/:id` | Outbound notifications. |
| `POST /cache/flush` | Drop this tenant's catalog cache. |
| `GET /keys`, `DELETE /keys/:id` | List and revoke. |
| `PATCH /tenants/:tenant`, `DELETE /tenants/:tenant` | Update, remove. |

Root-only, not tenant-scoped: `GET /admin/v1/meta`, `POST /admin/v1/catalog/refresh`,
`GET /admin/v1/audit`, and `POST /admin/v1/admin-keys` — which is how a
deployment rotates away from the bootstrap key in its environment, since that
one cannot be revoked without a restart.

Data plane, with a `cg-` key: `POST /v1/chat/completions`, `GET /v1/models`,
`GET /v1/models/*`, `GET /v1/usage`, `GET /v1/usage/breakdown`,
`GET /v1/health`, `GET /v1/meta`.

Routing is case-sensitive on purpose. `/V1/Chat/Completions` is a 404.

---

## Integrating an existing application

The whole integration is: **change the base URL and the key.** Any client that
speaks the OpenAI Chat Completions API already speaks CogniGate. Do not write
an adapter.

```python
from openai import OpenAI

client = OpenAI(
    base_url=os.environ["COGNIGATE_URL"] + "/v1",
    api_key=os.environ["COGNIGATE_API_KEY"],   # the cg- secret
)
```

```typescript
const client = new OpenAI({
    baseURL: process.env.COGNIGATE_URL + "/v1",
    apiKey: process.env.COGNIGATE_API_KEY,
});
```

When you are asked to wire CogniGate into an existing codebase, work in this
order and change nothing else:

1. **Find the call sites.** Grep for provider SDK constructors and raw
   endpoints: `OpenAI(`, `AzureOpenAI(`, `ChatOpenAI`, `api.openai.com`,
   `openai.azure.com`, `chat/completions`, and whatever environment variable
   currently holds the provider key.
2. **Inventory the model names in use.** Each becomes either a `pin` on an
   alias or a literal that must exist in some registered provider's catalog.
3. **Add two settings** to the application's existing configuration mechanism —
   the same place its current provider key lives: `COGNIGATE_URL` and
   `COGNIGATE_API_KEY`. Do not introduce a new config system for two strings.
4. **Repoint the client construction.** Base URL and key only. Leave request
   bodies, streaming, retries and error handling exactly as they are; the wire
   format is unchanged.
5. **Delete the provider credential from the application.** Remove it from
   `.env`, from the deployment's secret store, and from CI. If it was ever
   committed, it must be **rotated at the provider**, not merely deleted. This
   is the step that gets skipped, and it is the one that matters.
6. **Move model names behind aliases.** Replace a hardcoded model id at the
   call site with `"fast"` or `"best"`, and pin the alias in the gateway. The
   model a feature uses becomes an operational decision instead of a deploy.
7. **Verify against the running application**, not against curl: exercise a
   real request path, confirm `X-CogniGate-Served-By` on the response, and
   confirm a row in `GET /admin/v1/tenants/$TENANT/usage`.

Mint **one key per application, per environment**. A shared key cannot be
revoked without causing an outage somewhere else, and its usage cannot be
attributed to anything.

CogniGate is domain-agnostic. It routes LLM traffic and meters it; it knows
nothing about what the calling application does, and nothing in it needs to.

---

## Troubleshooting

| Symptom | Cause | What to do |
| --- | --- | --- |
| A service exits at startup | `GATEWAY_BOOTSTRAP_KEY` or `ANALYTICS_TOKEN` is still `replace_me`, or the bootstrap key is under 16 characters | Fill in `.env`, then `docker compose up -d` |
| `401` on `/admin/v1/**` | Wrong key, or a `cg-` data key used on the admin plane | Use the bootstrap key, or an admin-plane key |
| `403` on an admin route | A tenant-scoped admin key reaching outside its tenant | Root scope is required, and only a root key can mint one |
| `404 not_supported` | Path is not on the surface, or the case is wrong | Compare against the route tables above |
| `2xx` responses but no usage rows | `ANALYTICS_TOKEN` mismatch — the gateway drops the record after one 4xx | Grep gateway logs for `usage record could not be persisted`, make both sides equal, restart both |
| An alias write is refused | The name collides with a real model id in the catalog | Choose another name; the collision is the point |
| Requests route somewhere unexpected | The alias resolved differently than you assumed | Read `X-CogniGate-Served-By`, then `GET /aliases` and `POST /cache/flush` |
| A provider stops being tried | Its circuit breaker opened after repeated failures | Expected. It closes on its own — register a second provider so there is somewhere to fall back to |
| `429` | A tenant or key quota | `GET /quota`, and `GET /usage` for what consumed it |

Logs: `docker compose logs -f gateway`, and the same for `analytics` and
`postgres-db`.

---

## Keeping it running

- `GET /healthz` is the liveness probe. `GET /v1/health` reports provider
  reachability and needs a key.
- Metrics are Prometheus-format and off by default; enable them in
  `cognigate.config.yml` under `metrics`.
- Upgrade: `git pull && docker compose pull && docker compose up -d`. The
  published images are `ghcr.io/life-experimentalist/cognigate-gateway` and
  `ghcr.io/life-experimentalist/cognigate-analytics`, both multi-arch and both
  carrying build provenance you can check with `gh attestation verify`.
- Back up the Postgres volume. It holds every tenant, key hash, provider
  credential and usage record; nothing else in the stack is stateful.
- Rotate away from the bootstrap key once the deployment is real:
  `POST /admin/v1/admin-keys`, then drop `GATEWAY_BOOTSTRAP_KEY`.

## Where to read more

| Topic | Where |
| --- | --- |
| Full documentation | <https://life-experimentalist.github.io/CogniGate> |
| Machine-readable project context | `COGNIGATE_AI_CONTEXT.md` in the repo root |
| OpenAPI description | `openapi.yaml` in the repo root |
| Behavioural specification | `spec/gw-01…gw-14` in the repo |
