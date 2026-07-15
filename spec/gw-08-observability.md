# GW-8 — Observability: logs, metrics, events

**Status:** Specified · **Plane:** Operational · **Depends on:** GW-3, GW-4, GW-5

## Motivation

A router in front of every AI call is either a debugging superpower or a
black hole. Operators need to answer "what happened to request X",
"which tenant is burning budget", and "when did the breaker open" from
standard tooling — structured logs, Prometheus, and push events — without
CogniGate inventing a bespoke observability stack.

## Behavioral requirements

### Structured logs

- Both processes (gateway, analytics) MUST log JSON lines to stdout:
  one line per completed data-plane request on the gateway, plus
  lifecycle/warning lines. Minimum request-log fields:
  `ts`, `level`, `request_id` (== `X-CogniGate-Request-Id`),
  `client_request_id` (when supplied), `tenant`, `key_prefix`, `route`
  (endpoint), `alias` (when used), `provider`, `model`,
  `fallback_depth`, `status`, `error_code` (when any),
  `duration_ms`, `upstream_duration_ms`, `prompt_tokens`,
  `completion_tokens`.
- Logs MUST NOT contain: prompt or completion content, full API keys
  (only documented prefixes/fingerprints), or provider secrets (GW-14).
- Log level is configurable (`error|warn|info|debug`, default `info`);
  `debug` still obeys the content ban above.

### Metrics (Prometheus)

- `GET /metrics` on both processes, Prometheus text format, enabled by
  default; deployments MAY bind it to a separate port or protect it —
  it MUST NOT require a `cg-*`/`cga-*` key by default (scrapers are not
  tenants).
- Required series (names are normative; label sets are minimums):

  | Metric | Type | Labels |
  | ------ | ---- | ------ |
  | `cognigate_requests_total` | counter | `tenant, provider, model, route, code` |
  | `cognigate_request_duration_seconds` | histogram | `tenant, provider, route` |
  | `cognigate_upstream_duration_seconds` | histogram | `provider, model` |
  | `cognigate_tokens_total` | counter | `tenant, provider, model, direction=prompt\|completion` |
  | `cognigate_cost_usd_total` | counter | `tenant, provider, model` |
  | `cognigate_fallback_cascades_total` | counter | `tenant, from_model, to_model, reason` |
  | `cognigate_breaker_state` | gauge (0=closed,1=half-open,2=open) | `provider, model` |
  | `cognigate_catalog_age_seconds` | gauge | `provider` |
  | `cognigate_quota_state` | gauge (0=ok,1=soft,2=hard) | `tenant, window, unit` |
- Label values MUST be low-cardinality identifiers (tenant id, model id);
  never request ids or free text.

### Events / webhooks

- Admins register webhook endpoints per tenant and globally (GW-6:
  `/admin/v1/tenants/{id}/webhooks`, `/admin/v1/webhooks`), each with a
  URL, a shared secret, and an event filter.
- Event types (initial registry; extensible, unknown types must be
  ignorable by receivers):
  `quota.threshold_crossed`, `quota.hard_cap_reached`,
  `breaker.opened`, `breaker.closed`,
  `catalog.model_added`, `catalog.model_removed`,
  `alias.degraded`, `rule.degraded`.
- Delivery: `POST` JSON
  `{"id", "type", "created", "tenant", "data": {...}}` with headers
  `X-CogniGate-Event-Id` and
  `X-CogniGate-Signature: sha256=<HMAC-SHA256 of raw body with the
  endpoint secret>`. Receivers verify the signature; CogniGate MUST
  send events at-least-once with retries (5 attempts, exponential
  backoff from 5 s) and treat any 2xx as delivered. Event `id` is stable
  across retries so receivers can deduplicate.
- Threshold/breaker events fire **on transition**, not per request
  (shared rule with GW-4/GW-5).
- The last 1000 events per tenant MUST also be queryable via
  `GET /admin/v1/tenants/{id}/events` for consumers that prefer polling.

## Configuration surface

| Key                          | Default | Meaning |
| ---------------------------- | ------- | ------- |
| `log.level`                  | `info`  | `error\|warn\|info\|debug` |
| `metrics.enabled`            | `true`  | Serve `/metrics` |
| `metrics.listen`             | main listener | Optional separate bind |
| `webhooks.max_attempts`      | `5`     | Delivery retries |
| `webhooks.timeout`           | `10s`   | Per-delivery timeout |

## Acceptance criteria

- **GW-8.AC-1** — One completed chat completion produces exactly one
  gateway request-log line containing `request_id`, `tenant`, `provider`,
  `model`, `status`, `duration_ms`, and token counts; the `request_id`
  equals the response's `X-CogniGate-Request-Id`.
- **GW-8.AC-2** — No log line produced by the conformance run contains
  any prompt/completion substring planted by the suite, nor more of any
  `cg-*` key than its documented prefix.
- **GW-8.AC-3** — `GET /metrics` parses as Prometheus text format and,
  after N successful requests, `cognigate_requests_total` for the test
  tenant/model has increased by N.
- **GW-8.AC-4** — A forced fallback (GW-3.AC-2) increments
  `cognigate_fallback_cascades_total` with the correct
  `from_model`/`to_model`, and a forced breaker-open flips
  `cognigate_breaker_state` to 2.
- **GW-8.AC-5** — A registered webhook receives `breaker.opened` within
  30 seconds of the transition, with a valid HMAC signature over the raw
  body.
- **GW-8.AC-6** — With the webhook receiver returning 500 twice then
  200, the event arrives exactly once by id (retries observed, receiver
  dedupes by `X-CogniGate-Event-Id`).
- **GW-8.AC-7** — Crossing a quota soft threshold emits exactly one
  `quota.threshold_crossed` event per window (shared with GW-4.AC-2).

## Non-goals

- No bundled dashboards, alert rules, or log shipping — CogniGate emits;
  Grafana/Loki/etc. are the consumer's choice. (Example dashboards MAY
  ship in `docs/`, untested.)
- No OpenTelemetry trace export in this revision (a future requirement;
  the `request_id` field is designed to slot into a trace id later).
- No per-request webhook events (volume); events are state transitions
  only.
- Metrics carry no cost *attribution* logic beyond metering's numbers.
