# GW-14 — Content-blind design (privacy statement)

**Status:** Specified · **Plane:** All · **Depends on:** GW-8, GW-12

## Motivation

CogniGate sits in front of prompts that may contain anything — customer
records, legal drafts, source code. The only privacy posture that scales
across unrelated downstream projects is **content-blindness**: the
gateway routes, meters, and observes *metadata*, and treats message
content as opaque payload it neither inspects, persists, nor emits.
Domain-specific redaction (PII masking, field-level scrubbing) is
explicitly the consumer's job, done *before* the request reaches CogniGate — a
gateway that promised to understand everyone's compliance regime would
deliver none of them.

## Behavioral requirements

### The content ban

- CogniGate MUST NOT persist prompt or completion content to any durable
  store (Postgres, files) in normal operation. Usage records, audit
  logs, request logs, metrics, and events carry **metadata only**:
  identifiers, token counts, model/provider, timings, status, cost.
- CogniGate MUST NOT emit content in: logs at any level including
  `debug` (GW-8), error envelopes (GW-3's `upstream_exhausted` body
  lists failure classes, not payloads), webhook events, or metrics
  labels.
- Content transits gateway memory and, when GW-12 caching is enabled,
  lives in Redis under the entry TTL — both documented, bounded, and
  under the operator's control. Telemetry from gateway to analytics
  MUST already exclude content (contractualizing existing behavior).
- CogniGate MUST NOT parse message content for routing, moderation, or
  analytics beyond what proxying strictly requires (JSON framing, token
  accounting via provider-reported `usage`, SSE re-framing).

### Debug capture (the one exception, off by default)

- A deployment MAY enable **debug capture** per tenant via GW-6:
  `debug_capture: {enabled, ttl, sample_rate}`.
  - Default: `enabled: false`. Enabling requires an explicit admin
    action, is recorded in the admin audit log (GW-6), and fires an
    `events`-visible record.
  - Captured request/response bodies are stored encrypted at rest (same
    AES-256-GCM vault key discipline as provider keys), scoped to
    tenant, retrievable only via admin plane
    (`GET /admin/v1/tenants/{id}/captures`), and **hard-deleted** at
    `ttl` — maximum **72 h**, no override.
  - `sample_rate` defaults to `0.01`; `1.0` is allowed but the API
    response to enabling it MUST echo a warning field.
  - While capture is enabled for a tenant, every data-plane response for
    that tenant MUST carry `X-CogniGate-Debug-Capture: on` — consumers
    can see, per response, that retention is active. (Header absent
    otherwise.)

### Statement for adopters

Documentation MUST carry a plain-language version of this section
("what CogniGate sees and stores") that a downstream project can cite in
its own compliance documentation. CogniGate makes no regulatory claims
of its own under any regime; it provides the content-blind substrate on
which consumers build theirs.

## Configuration surface

| Key                                   | Default | Meaning |
| ------------------------------------- | ------- | ------- |
| Per-tenant `debug_capture.enabled`    | `false` | Via GW-6 only |
| Per-tenant `debug_capture.ttl`        | `24h` (max `72h`) | Hard-delete horizon |
| Per-tenant `debug_capture.sample_rate`| `0.01`  | Fraction captured |

There is deliberately no deployment-wide "capture everything" switch.

## Acceptance criteria

- **GW-14.AC-1** — After a conformance run planting unique sentinel
  strings in prompts and (mock) completions, no sentinel appears in:
  Postgres dumps, gateway/analytics stdout logs, `GET /metrics` output,
  webhook deliveries, or any admin-plane response — with debug capture
  off (superset of GW-8.AC-2).
- **GW-14.AC-2** — With debug capture off (default), the captures
  endpoint returns an empty list after traffic flows.
- **GW-14.AC-3** — Enabling capture (`sample_rate: 1.0`, `ttl: 60s` for
  test) makes captured bodies retrievable via the admin plane, marks
  every response `X-CogniGate-Debug-Capture: on`, and the entries are
  gone (404/absent) after the TTL elapses.
- **GW-14.AC-4** — Attempting `debug_capture.ttl` > 72 h is rejected
  with 400.
- **GW-14.AC-5** — The enable/disable actions appear in
  `GET /admin/v1/audit` (GW-6.AC-8 companion).
- **GW-14.AC-6** — A captured body read back via the admin plane matches
  the sentinel; a raw Postgres inspection of the same row shows
  ciphertext, not the sentinel (encryption at rest).
- **GW-14.AC-7** — GW-3's `upstream_exhausted` error body for a request
  containing a sentinel does not contain the sentinel.

## Non-goals

- No PII detection, masking, or redaction — by design, not by
  omission; that intelligence belongs upstream in the consumer (e.g. the
  consumer pseudonymizes identifiers before calling CogniGate).
- No content moderation or safety filtering.
- No guarantee about what **providers** retain — CogniGate's contract
  ends at the upstream call; provider data-use terms are the operator's
  provider choice.
- No end-to-end encryption of content through the gateway (it must read
  the JSON to proxy it); TLS on both hops (GW-11) is the transport
  story.
