# GW-13 — Request/response size limits & time budgets

**Status:** Specified · **Plane:** Data + admin · **Depends on:** GW-7

## Motivation

An unbounded proxy is a denial-of-service amplifier: one tenant's 200 MB
payload or never-ending stream can starve everyone. Limits must be
explicit, per-tenant tunable, discoverable via meta (GW-9), and fail
with distinct codes clients can handle — not mystery connection resets.

## Behavioral requirements

### Size limits

- **Request body:** default maximum **2 MiB** (`2097152` bytes),
  configurable per deployment and per tenant (tenant ≤ deployment
  ceiling). Oversized requests are rejected with HTTP 413,
  `error.code = "request_too_large"`, before any upstream call and
  without buffering the excess.
- **Response body (non-streaming):** default maximum 8 MiB from
  upstream; beyond it the gateway MUST abort with 502
  `error.code = "response_too_large"` rather than buffer unboundedly.
- **Streaming:** no total-size cap, but an **idle timeout** (no bytes
  from upstream) of default 60 s aborts the stream with a terminal
  error event `error.code = "upstream_stream_stalled"`.
- Limits in force MUST be reported in `GET /v1/meta` `limits`
  (GW-9): `max_request_bytes`, `max_response_bytes`,
  `request_timeout_seconds`.

### Time budgets

- **Total request budget:** default **120 s** wall clock per data-plane
  request, spanning the entire GW-3 cascade (shared rule). Exceeding it
  returns 504 `error.code = "gateway_timeout"` (or a terminal stream
  event if streaming has begun).
- **Per-upstream-attempt connect timeout:** default 10 s.
- Budgets are configurable per deployment and per tenant (tenant ≤
  deployment ceiling).

### Concurrency

- Per-key in-flight request limit, default **32**; excess requests get
  429 `error.code = "concurrency_exceeded"` (distinct from quota codes —
  clients may retry immediately-ish, suggested after 1 s; `Retry-After: 1`
  is sent).
- The existing per-tenant rate limits
  (`rate_limit.requests_per_second: 50`, `burst_capacity: 100` in
  `cognigate.config.yml`) remain; when tripped they return 429
  `error.code = "rate_limited"` with `Retry-After`.

## Configuration surface

| Key                                  | Default | Meaning |
| ------------------------------------ | ------- | ------- |
| `limits.max_request_bytes`           | `2097152` | Deployment ceiling |
| `limits.max_response_bytes`          | `8388608` | Non-streaming upstream cap |
| `limits.request_timeout`             | `120s`  | Total budget incl. fallback cascade |
| `limits.upstream_connect_timeout`    | `10s`   | Per attempt |
| `limits.stream_idle_timeout`         | `60s`   | Streaming stall abort |
| `limits.max_concurrent_per_key`      | `32`    | In-flight cap |
| Per-tenant overrides (≤ ceilings)    | unset   | Via GW-6 |

## Acceptance criteria

- **GW-13.AC-1** — A request body of `max_request_bytes + 1` returns 413
  `request_too_large`; the mock provider records zero calls.
- **GW-13.AC-2** — A mock upstream returning > `max_response_bytes`
  yields 502 `response_too_large`, and gateway RSS does not grow by the
  response size (no full buffering; asserted approximately in `-perf`
  mode, exact code path otherwise).
- **GW-13.AC-3** — A mock upstream that stalls a stream for >
  `stream_idle_timeout` produces a terminal error event
  `upstream_stream_stalled` and closes the stream.
- **GW-13.AC-4** — A cascade whose entries each consume most of the
  budget is cut off at `request_timeout` with 504 `gateway_timeout`;
  total observed wall time ≤ budget + 5 s.
- **GW-13.AC-5** — The 33rd concurrent request on one key returns 429
  `concurrency_exceeded` with `Retry-After`, while a second key
  proceeds.
- **GW-13.AC-6** — `GET /v1/meta` `limits` values equal the behavior
  observed in AC-1..AC-4 (shared with GW-9.AC-3).
- **GW-13.AC-7** — The four 429-family codes (`rate_limited`,
  `concurrency_exceeded`, `quota_exceeded`, `budget_exceeded`) are
  produced by their four distinct triggers and never conflated.

## Non-goals

- No token-count-based admission (counting tokens requires reading the
  prompt deeply; bytes are the limit unit — see GW-14's minimal-
  inspection stance). Token limits belong to quotas (GW-4), enforced on
  metered results.
- No global (cross-tenant) fairness scheduler in this revision; limits
  are per key/tenant.
- No request queuing — over-limit is rejected, not parked.
