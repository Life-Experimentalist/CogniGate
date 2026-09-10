---
name: cognigate
description: Use when installing, operating, or wiring an application into CogniGate — the self-hosted OpenAI-compatible LLM gateway. Covers first install, tenant and key provisioning, provider registration, routing aliases, quotas, rate limits and concurrency caps, what a tenant is charged and how much of the cost it can see, a system instruction put in front of every prompt, health checks, and the integration path for any codebase that already calls an LLM API.
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
   volume, and with it every usage record. Tenants, keys and providers are not
   in there — a plain gateway restart already loses those, see Provision.
   Neither is the right first response to a failing container.

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

**Everything created below is held in memory.** Tenants, keys, providers,
aliases, routes and quotas live in the gateway process; Postgres holds usage
records and nothing else. Restarting or recreating the gateway container —
`docker compose pull` and `up -d`, a config change, a crash — returns the
control plane to a clean slate, and the first symptom is `401
invalid_api_key` on a key that worked a minute ago. Two consequences you have
to act on: keep the provisioning calls in a script so re-creating a deployment
is one command, and never restart the gateway to "fix" something after someone
has added a provider credential, because that credential goes with it.

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
| `base_url` | Required, unless `kind` is one that has a known endpoint. A trailing slash is stripped. |
| `keys` | One or more; at least one is required. See `key_strategy`. |
| `kind` | Optional, defaults to `openai`. Also `gemini`, `anthropic`. |
| `key_strategy` | Optional, `round_robin` (default) or `failover`. |
| `enabled` | Optional, defaults to `true`. |

Three kinds are registered. `openai` covers every endpoint that reimplements
the OpenAI wire format (Together, Groq, Fireworks, Azure OpenAI, OpenRouter,
vLLM, Ollama, LM Studio) and an unrecognised `kind` falls back to it.
`gemini` and `anthropic` are the same adapter pointed at each vendor's
OpenAI-compatible endpoint, and they carry that endpoint as their default, so
those two can be registered without a `base_url`:

```bash
curl -sS -X POST $CG/admin/v1/tenants/$TENANT/providers -H "Authorization: Bearer $CG_ADMIN" -H 'Content-Type: application/json' -d '{"name":"gemini","kind":"gemini","keys":["THE_PROVIDER_KEY"]}'
```

Anthropic's compatibility layer is documented by Anthropic as a way to
evaluate Claude with OpenAI client code rather than as a production API, and it
silently ignores the request fields it does not implement (`response_format`,
`seed`, `logprobs`, `presence_penalty`, `frequency_penalty`) instead of
rejecting them. Requests succeed; a caller depending on one of those fields
gets an answer computed without it. Register `kind: anthropic` knowing that.

`key_strategy` decides which pooled key a request starts from. `round_robin`,
the default, advances one key per request, so four keys take a quarter of the
load each. `failover` always starts at the first key and reaches the others
only when it is rate limited, which is what you want when the keys are not
equivalent, such as a paid key backed by a free one. Under both, a 429 walks
the rest of the pool before the request cascades to another provider, so no
working credential is left untried. Registering a second provider is how that
cascade gets somewhere to go.

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
| `PUT /quota`, `GET /quota`, `DELETE /quota` | Per-tenant ceilings. Windows `day` and `month`, units `tokens`, `cost` and `requests`, each a `cap` with an optional `soft_threshold_pct`. |
| `PUT /keys/:id/quota` and friends | The same, narrowed to one key. Both are evaluated, so a key cap can only narrow what the tenant is already allowed, never widen it. |
| `GET /usage`, `GET /usage/breakdown` | Metered consumption. |
| `GET /events`, `GET /captures` | What happened, and captured requests for debugging. |
| `POST /webhooks`, `GET`, `DELETE /webhooks/:id` | Outbound notifications. |
| `POST /cache/flush` | Drop this tenant's catalog cache. |
| `GET /keys`, `DELETE /keys/:id` | List and revoke. |
| `PATCH /tenants/:tenant`, `DELETE /tenants/:tenant` | Update, remove. The body takes `name`, `status`, `limits`, `cache`, `debug_capture` and `system_instruction`, each optional and each replacing what is there. |

Root-only, not tenant-scoped: `GET /admin/v1/meta`, `POST /admin/v1/catalog/refresh`,
`GET /admin/v1/audit`, and `POST /admin/v1/admin-keys` — which is how a
deployment rotates away from the bootstrap key in its environment, since that
one cannot be revoked without a restart.

Data plane, with a `cg-` key: `POST /v1/chat/completions`, `GET /v1/models`,
`GET /v1/models/*`, `GET /v1/usage`, `GET /v1/usage/breakdown`,
`GET /v1/health`, `GET /v1/meta`.

Routing is case-sensitive on purpose. `/V1/Chat/Completions` is a 404.


---

## Operator settings

The settings below live in `cognigate.config.yml` and are what an operator
tunes once the install works. The file is bind-mounted, so changing it needs
`docker compose up -d --force-recreate gateway`, which empties the control
plane along with everything else held in memory. Set them before provisioning,
not after. The billing and chat settings below also have an environment
spelling, which is what a deployment that keeps its configuration in a secret
store uses instead; both `CG_NAME` and a bare `NAME` are read. The rate and
concurrency figures have none, and are set in the file or per tenant through
`PATCH /admin/v1/tenants/:tenant`.

**What the tenant is charged.** `cost_usd` is what the provider charged the
operator; `charge_usd` is what the tenant owes. `billing.mode` is the whole
difference between them.

```yaml
billing:
  mode: passthrough # passthrough | markup | absorb
  markup_pct: 0 # only read in markup mode
  cost_visibility: hidden # hidden | hint | exact
```

`passthrough` charges the provider rate itself, so the operator collects
nothing and there is no second price table to keep current. `markup` charges
that plus `markup_pct`, and a `markup` mode with no percentage is refused at
startup rather than quietly behaving as passthrough. `absorb` charges nothing
at all: `charge_usd` is zero on every row and the operator carries the bill. A
cost quota is measured in cost rather than charge, so a spend cap still stops
runaway usage under `absorb`.

`cost_visibility` decides how much of `cost_usd` the data plane publishes.
`hidden` is the default and publishes none of it: `/v1/usage` and
`/v1/usage/breakdown` carry no `cost_usd` key at all, and a `cost` quota slot
is left out of `limits` with it, because `cap` minus `remaining` gives the
consumption back. The tenant is still told where it stands: the overall
`state` accounts for the dropped slot, so a tenant on a spend cap reads
`hard-exceeded` and is still rejected with `budget_exceeded`. `hint` publishes
the cost coarsened to one significant figure instead, so a tenant sees the
order of magnitude of what its traffic cost but cannot divide the charge
beside it by an exact cost and recover the operator's margin. `exact`
publishes the computed figure.

The charge is never rounded or withheld, because it is what the tenant owes;
the admin plane and the stored row stay exact whatever this is set to; and
enforcement reads the exact position, so what stops traffic never depends on
how it is displayed.

Environment: `CG_BILLING_MODE`, `CG_BILLING_MARKUP_PCT`,
`CG_BILLING_COST_VISIBILITY`.

**Rate limits, a concurrency cap, and quotas.** Three different things, and
reaching for the wrong one is the usual reason a limit does not do what an
operator expected.

```yaml
rate_limit:
  requests_per_second: 50 # token bucket, per tenant
  burst_capacity: 100 # how far above it a burst may go
  requests_per_minute: 3600 # fixed minute window; 0 switches it off
limits:
  max_concurrent_per_key: 32 # requests in flight, per key
```

`requests_per_second` with `burst_capacity` is a bucket that refills
continuously: it smooths traffic, but a caller that has been idle can spend
the whole burst at once. `requests_per_minute` is a plain count over a fixed
window, which is how a provider allowance is usually written, so set it when
you are trying to stay under one. The shipped 3600 clears what the bucket
admits in a minute at the two figures above, so out of the box it never
refuses a request the bucket would have allowed; lower it to make it bind.

Both are per tenant rather than per key, because a tenant that could lift its
own ceiling by minting another key would not have a ceiling.
`max_concurrent_per_key` goes the other way on purpose: it bounds how many
requests one key may have in flight, so one integration cannot starve another
inside the same tenant. Reach for it when a client opens twenty streams,
because a rate limit counts arrivals and a long stream arrives once.

A per-day allowance is a quota rather than a rate limit. It resets at midnight
UTC, reads back with a remaining figure, can be narrowed to one key, and can
be run in observe mode first: `quotas.enforcement: observe`, or
`CG_QUOTA_ENFORCEMENT=observe`, emits the events and the headers without
rejecting anything, which is how to size a cap before it starts biting.

```bash
curl -sS -X PUT $CG/admin/v1/tenants/$TENANT/quota \
  -H "Authorization: Bearer $CG_ADMIN" -H 'Content-Type: application/json' \
  -d '{"day":{"requests":{"cap":50000}},"month":{"cost":{"cap":250,"soft_threshold_pct":80}}}'
```

Windows are `day` and `month`, units are `tokens`, `cost` and `requests`, and
each unit is a `cap` with an optional `soft_threshold_pct`. A unit left out is
unlimited rather than capped at zero. Only served requests count against a
`requests` cap: anything a rate limit, a quota or the concurrency cap refused
writes no usage row, so retrying against a full cap does not dig the hole
deeper.

Any of the rate and concurrency figures can be overridden for one tenant
through `PATCH /admin/v1/tenants/:tenant` with a `limits` object. That object
replaces the whole block rather than merging into it, so send every override
the tenant should have, and an empty object clears them all.

**A system instruction in front of every prompt.**

```yaml
chat:
  system_instruction: "" # empty, the default, adds nothing
```

The text goes out as a system message ahead of whatever the caller sent. A
tenant can be given its own through `PATCH /admin/v1/tenants/:tenant` with
`system_instruction`, and then both are sent as one message with the
deployment's text first. An empty string there is a value rather than an
omission: it takes the tenant's own text away again. Neither is merged into a
system message the caller wrote, which still arrives after both, unchanged.

Three things to tell an operator before they set one:

- It is prompt text. The provider counts its tokens on every request, so it
  lands in `cost_usd` and in whatever the tenant is charged.
- A caller cannot opt out. The rewrite happens before the cache key is
  computed and before the buffered and streamed paths split.
- What an upstream does with two system messages is the upstream's business.
  Anthropic's compatibility endpoint documents joining them into one, in
  order, so a configured instruction still leads. Google states no rule.

Environment: `CG_CHAT_SYSTEM_INSTRUCTION`.

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
| `429` | A quota, a rate limit, or the per-key concurrency cap | The error code says which: `quota_exceeded`, `budget_exceeded`, `rate_limited` or `concurrency_exceeded`. Then `GET /quota`, and `GET /usage` for what consumed it |
| The catalog is empty and a provider is registered | Every provider failed to refresh, most often a key the provider refuses | `GET /v1/health`, and read `catalog.error`. It names each provider and its reason, and quotes no key material |
| `prompt_tokens` is higher than what the caller sent | A system instruction is configured, and it is prompt text on every request | Expected. Check `chat.system_instruction` and the tenant's own, and remember both are charged |
| Spend looks low against the request count | The cache answered some of them, at no tokens and no cost | Read `cached_requests` on the same usage response. That is a working cache, not a metering fault |

Logs: `docker compose logs -f gateway`, and the same for `analytics` and
`postgres-db`.

---

## Keeping it running

- `GET /healthz` is the liveness probe. `GET /v1/health` reports provider
  reachability and needs a key.
- Metrics are Prometheus-format and off by default; enable them in
  `cognigate.config.yml` under `metrics`. The file is bind-mounted, so the
  change needs `docker compose up -d --force-recreate gateway` to take — which
  empties the control plane. Re-provision afterwards, see Provision.
- Metering is asynchronous and bounded, and the queue is per-gateway rather than
  per-tenant. If `cognigate_telemetry_dropped_total` is climbing, or the logs say
  the telemetry buffer is full, the gateway is serving faster than analytics can
  record; usage and quotas both under-count until it catches up. Raise
  `telemetry.buffer` for bursts, lower `rate_limit` for a sustained gap. Enough
  tenants at the default rate reach the same ceiling between them, with nobody
  having raised anything.
- Upgrade: `git pull && docker compose pull && docker compose up -d`. That
  recreates the gateway container, so re-provision afterwards — see Provision.
  The published images are `ghcr.io/life-experimentalist/cognigate-gateway` and
  `ghcr.io/life-experimentalist/cognigate-analytics`, both multi-arch and both
  carrying build provenance you can check with `gh attestation verify`.
- Back up the Postgres volume. It holds the usage records, which are the only
  state in the stack that outlives a restart. What protects a tenant, key or
  provider is the script that created it, not a backup.
- Rotate away from the bootstrap key once the deployment is real:
  `POST /admin/v1/admin-keys`, then drop `GATEWAY_BOOTSTRAP_KEY`.

## Where to read more

| Topic | Where |
| --- | --- |
| Full documentation | <https://cognigate.vkrishna04.me> |
| Machine-readable project context | `COGNIGATE_AI_CONTEXT.md` in the repo root |
| OpenAPI description | `openapi.yaml` in the repo root |
| Behavioural specification | `spec/gw-01…gw-14` in the repo |
