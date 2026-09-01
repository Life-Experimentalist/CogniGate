# GW-5 — Health & honest degradation

**Status:** Specified · **Plane:** Data · **Depends on:** GW-1, GW-2, GW-3

## Motivation

Downstream products need to render a truthful "AI degraded / AI
unavailable" state *before* users hit per-request timeouts. That requires
a cheap, tenant-scoped health endpoint that reports what the gateway
actually knows: breaker states, catalog freshness, degraded rules — not a
bare 200.

## Behavioral requirements

### Endpoint

- `GET /v1/health` on the data plane, authenticated with a `cg-*` key,
  scoped to **what the calling tenant may see**: only providers, aliases,
  and rules that tenant uses. Tenant A MUST NOT learn that tenant B
  exists or which providers B uses.
- The endpoint MUST respond from gateway-local state (Redis / memory) in
  under 100 ms at p99; it MUST NOT fan out live calls to providers.
- Response shape:

  ```json
  {
    "status": "ok",
    "gateway": { "version": "1.4.0", "uptime_seconds": 86400 },
    "providers": [
      {
        "provider": "openai",
        "breaker": "closed",
        "catalog": { "age_seconds": 312, "state": "fresh" }
      },
      {
        "provider": "anthropic",
        "breaker": "open",
        "breaker_until": "2026-09-01T10:05:00Z",
        "catalog": { "age_seconds": 25000, "state": "stale" }
      }
    ],
    "aliases": [
      { "name": "fast", "state": "ok", "resolves_to": "llama-3.3-70b" },
      { "name": "transcribe", "state": "degraded", "reason": "alias_unresolvable" }
    ],
    "quota": { "state": "ok" }
  }
  ```

### Status computation

- `status` MUST be one of `ok | degraded | unavailable`, computed as:
  - `unavailable` — no provider usable by this tenant has a closed or
    half-open breaker (every path is dead), or the gateway cannot reach
    its own state store.
  - `degraded` — any of: a breaker open, catalog `stale`
    (age > `catalog.stale_warn_after`, GW-1), any alias or routing rule
    flagged degraded (GW-2/GW-3), or quota state `hard-exceeded`.
  - `ok` — otherwise.
- Breaker states MUST be reported per provider (and per provider/model
  where the breaker is model-scoped) as `closed | open | half-open`,
  with `breaker_until` when open.
- HTTP status: 200 for `ok` and `degraded`, 503 for `unavailable` — so
  the endpoint works both for dashboards (parse the body) and dumb
  probes (check the code).
- The unauthenticated liveness endpoint `GET /healthz` (no tenant data,
  body `{"status":"ok"}`) MUST exist for container orchestration; it
  reports only that the gateway process is serving.

### Change signaling

- Breaker transitions (`closed→open`, `open→half-open`, `→closed`) MUST
  emit `breaker.opened` / `breaker.closed` events (GW-8) so consumers can
  push state instead of polling.
- Health output MUST reflect a breaker transition within 5 seconds of it
  occurring.

## Configuration surface

| Key                                   | Default | Meaning |
| ------------------------------------- | ------- | ------- |
| `health.cache_ttl`                    | `2s`    | Per-tenant health response cache |
| `catalog.stale_warn_after`            | `6h`    | Shared with GW-1; drives `catalog.state` |

## Acceptance criteria

- **GW-5.AC-1** — `GET /v1/health` with a valid key returns 200 with
  `status`, `providers[]` (each with `breaker` and `catalog.age_seconds`),
  and `aliases[]`; with no key it returns 401.
- **GW-5.AC-2** — `GET /healthz` returns 200 with no authentication and
  contains no provider or tenant information.
- **GW-5.AC-3** — Forcing a provider's breaker open (mock failures per
  GW-3) flips that provider to `"breaker": "open"` and overall `status`
  to `degraded` within 5 seconds.
- **GW-5.AC-4** — When every provider a tenant can use has an open
  breaker, `GET /v1/health` returns HTTP 503 with
  `status = "unavailable"`.
- **GW-5.AC-5** — A tenant's health response never names a provider,
  alias, or rule belonging exclusively to another tenant.
- **GW-5.AC-6** — With catalog refresh blocked past
  `catalog.stale_warn_after`, `catalog.state` becomes `stale` and
  `status` becomes `degraded`.
- **GW-5.AC-7** — 100 sequential health calls complete with p99 < 100 ms
  against a local deployment, and a mock provider records zero calls
  triggered by them.

## Non-goals

- Not a metrics endpoint — time series live in GW-8 (`/metrics`); health
  is current-state only.
- No synthetic probing of providers (no test completions on a timer);
  health reflects passively observed state. Active canary probing, if
  ever wanted, is a separate opt-in feature.
- No historical uptime reporting or SLA computation.
