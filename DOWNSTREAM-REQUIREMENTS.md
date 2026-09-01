# Downstream Integration Requirements (GW-1 … GW-14)

Requirements contributed by and written for downstream consumers of
CogniGate. They are deliberately **generic** — nothing here is specific
to any one application — so that any product embedding CogniGate (a
SaaS backend, an internal developer tool, a batch pipeline) can build
against the same contract. They extend, not replace, the Phase-1
scaffolding in [TODOS.md](TODOS.md) (key rotation, circuit breaker,
vault, metering).

**IDs are stable.** Consumers reference requirements as `GW-<n>` and
acceptance criteria as `GW-<n>.AC-<m>`; those identifiers never change
meaning. Each requirement has a full specification under
[`spec/`](spec/), with: motivation, behavioral requirements,
configuration surface, acceptance criteria (conformance-testable,
GW-10), and explicit non-goals.

## Requirement index

| ID | Title | One-line summary |
| -- | ----- | ---------------- |
| [GW-1](spec/gw-01-model-discovery.md) | Dynamic model discovery | Model catalogs are polled from providers on a TTL (default 1 h), served per-tenant via `GET /v1/models`, never hardcoded anywhere. |
| [GW-2](spec/gw-02-capability-aliases.md) | Capability aliases | Clients express intent (`fast`, `best`, `transcribe`); admin-defined per-tenant rules resolve to live catalog models; new models flow in with zero client changes. |
| [GW-3](spec/gw-03-fallback-chains.md) | Fallback chains | Ordered, validated failover — adjacent entries must resolve to *different models* — with truthful `X-CogniGate-Served-By` attribution. |
| [GW-4](spec/gw-04-quota-budget.md) | Quota & budget API | Per-tenant/per-key token and cost caps (day/month) with soft thresholds, distinct 429 codes, and a query API dashboards can render directly. |
| [GW-5](spec/gw-05-health-degradation.md) | Health & honest degradation | Tenant-scoped `GET /v1/health`: breaker states, catalog freshness, degraded aliases — so consumers show a truthful "AI degraded" state. |
| [GW-6](spec/gw-06-admin-api.md) | Admin/config API | Full CRUD over tenants, keys, provider keys, rules, aliases, quotas on a separate admin plane (`cga-*` keys) — consoles are built downstream, not in YAML. |
| [GW-7](spec/gw-07-client-contract.md) | Client/SDK contract | Exactly what a downstream client implements: base URL + `cg-*` key, supported endpoints, extension headers, retry semantics, and the no-hardcoded-model-ids rule. |
| [GW-8](spec/gw-08-observability.md) | Observability | JSON logs with request ids, normative Prometheus metrics, and HMAC-signed webhooks for quota/breaker/catalog transitions. |
| [GW-9](spec/gw-09-versioning.md) | Versioning & compatibility | Semver for the API surface, `GET /v1/meta` capability feature-detection, and a next-major-plus-6-months deprecation policy. |
| [GW-10](spec/gw-10-conformance.md) | Conformance test suite | A runnable suite (Go tests + container) any project points at any deployment; 1:1 with every AC below; doubles as CogniGate's regression harness. |
| [GW-11](spec/gw-11-deployment.md) | Deployment story | docker-compose sidecar as reference, zero-config `--dev` mode, static gateway binary, TLS options, footprint expectations, graceful drain. |
| [GW-12](spec/gw-12-response-caching.md) | Response caching (optional) | Exact-match, tenant-scoped caching of deterministic (`temperature: 0`) requests; off by default, always labeled `X-CogniGate-Cache`. |
| [GW-13](spec/gw-13-size-limits.md) | Size limits & time budgets | 2 MiB request cap, 120 s total budget across the fallback cascade, stream-stall timeouts, per-key concurrency — all discoverable via meta. |
| [GW-14](spec/gw-14-privacy.md) | Content-blind design | CogniGate never inspects, persists, or emits prompt/completion content; opt-in encrypted debug capture only, max 72 h retention. |

## Shared conventions (normative for every spec)

- **RFC 2119.** MUST/SHOULD/MAY in `spec/` carry their RFC 2119
  meanings.
- **Two planes, two key formats.** Data plane `/v1/*` with
  `Authorization: Bearer cg-…` (per-tenant); admin plane `/admin/v1/*`
  with `cga-…` keys. Neither key works on the other plane (GW-6).
- **Error envelope.** Every error, both planes, is OpenAI-shaped so
  stock SDKs parse it:

  ```json
  { "error": { "message": "…", "type": "…", "code": "…", "param": null } }
  ```

  `error.code` registry (HTTP status → codes):
  `401` `invalid_api_key`, `wrong_plane` ·
  `404` `model_not_found`, `alias_unresolvable`, `not_supported` ·
  `400` `fallback_duplicate_model` (+ validation codes) ·
  `409` `alias_collides_with_model` ·
  `413` `request_too_large` ·
  `429` `rate_limited`, `concurrency_exceeded`, `quota_exceeded`, `budget_exceeded` ·
  `502` `upstream_exhausted`, `response_too_large` ·
  `504` `gateway_timeout`.
  New codes are additive (GW-9); clients treat unknown codes by HTTP
  status.
- **Extension headers.** All CogniGate response headers use the
  `X-CogniGate-` prefix: `-Request-Id`, `-Served-By`
  (`<provider>/<model>`), `-Fallback-Depth`, `-Quota-State`, `-Cache`,
  `-Debug-Capture`, `-Event-Id`, `-Signature`, `-Deprecation`. Clients
  never *need* them for correct operation (GW-7).
- **Truthful attribution.** Responses, usage records, and logs always
  name the model/provider that actually served — never the alias or the
  originally requested model (GW-2/GW-3).
- **Config changes propagate in ≤ 10 s** without restart, via the
  existing Redis `cognigate:cache:invalidate` channel (GW-6).
- **Testability.** Every behavioral requirement has at least one
  acceptance criterion, and GW-10's suite maps 1:1 onto the AC ids —
  a requirement that can't be asserted against a live deployment
  doesn't belong here.
- **Tolerant readers.** Clients ignore unknown JSON fields, headers,
  and event types; that is what makes MINOR releases additive (GW-9).

## Follow-on work (tracked, not part of this spec)

- `openapi.yaml` currently describes only `POST /v1/chat/completions`;
  it must grow to cover the full GW-1..GW-14 surface and become the
  artifact GW-10 validates response shapes against.
- `conformance/` (GW-10) and the mock provider do not exist yet.
