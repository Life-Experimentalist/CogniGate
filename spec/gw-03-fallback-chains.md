# GW-3 — Fallback chains (different model required)

**Status:** Specified · **Plane:** Data + admin · **Depends on:** GW-1, GW-2

## Motivation

A single provider outage must not take a downstream product's AI features
down. But naive retry-same-model failover is worthless (the same model
fails the same way) and dishonest failover is dangerous (an audit trail
that says "gpt-4o answered" when a smaller model did is a lie). This
requirement mandates ordered, validated, *different-model* fallback with
truthful attribution.

## Behavioral requirements

### Chain definition

- Every routing rule MAY declare an **ordered fallback chain**, N deep
  (N ≥ 0; implementations MUST support at least N = 5). Entries are
  concrete model ids or aliases (GW-2).
- **Different-model validation rule:** an entry MAY use the same provider
  as the entry before it, but MUST resolve to a **different model**.
  Same provider + same model in adjacent positions is a configuration
  error:
  - rejected at rule save time (400 `error.code =
    "fallback_duplicate_model"`), and
  - re-checked whenever alias resolution changes (a catalog refresh or
    alias edit that collapses two adjacent entries onto one model marks
    the rule degraded in GW-5/GW-6 and skips the duplicate at runtime).

### Cascade triggers

The request MUST cascade to the next chain entry when the current entry
fails with any of:

1. Provider connection error / timeout.
2. Provider 5xx.
3. Provider 429 **after** in-pool key rotation is exhausted for that
   provider (rotation happens first; fallback is the escalation).
4. Circuit breaker open for that provider/model (the entry is skipped
   without an upstream attempt).
5. Model no longer present in the catalog (GW-1 removal semantics).

The request MUST NOT cascade on: provider 4xx caused by the request
itself (400 invalid payload, 413 too large upstream, content-policy
rejections) — those are returned to the client unchanged, because a
different model would fail identically or mask a real client bug.

### Budget and termination

- Total time across the cascade MUST respect the request's overall
  timeout (GW-13); remaining entries are abandoned when the budget is
  spent.
- When every entry fails, the gateway MUST return 502
  `error.code = "upstream_exhausted"` with a body listing, per attempted
  entry, the provider, model, and failure class (never the raw provider
  key or full provider error internals).
- For **streaming** requests, fallback is permitted only before the first
  byte of the response body is sent. Once streaming to the client has
  begun, a mid-stream provider failure terminates the stream with an
  error event; the gateway MUST NOT splice a second model into an
  in-progress stream.

### Truthful attribution

- Every successful response MUST report which model/provider actually
  served it:
  - Header `X-CogniGate-Served-By: <provider>/<model-id>`
  - Header `X-CogniGate-Fallback-Depth: <n>` (0 = primary served)
  - Response body `model` field = the serving model id
  - The usage record (GW-4/GW-8) stores the serving provider+model, and
    `fallback_depth`.

## Configuration surface

| Key                                   | Default | Meaning |
| ------------------------------------- | ------- | ------- |
| `routing.max_fallback_depth`          | `5`     | Hard cap on chain length accepted at save time |
| `routing.breaker.error_threshold`     | `5` errors / `30s` | Failures that open a provider/model breaker |
| `routing.breaker.open_duration`       | `60s`   | Time before a half-open probe |
| Per-rule `fallback_chain`             | `[]`    | Ordered entries; managed via GW-6 |

## Acceptance criteria

- **GW-3.AC-1** — Saving a rule with adjacent entries resolving to the
  same provider+model returns 400 `fallback_duplicate_model`.
- **GW-3.AC-2** — With the primary (mock) provider returning 500, a
  request succeeds via the second entry;
  `X-CogniGate-Fallback-Depth: 1` and `X-CogniGate-Served-By` names the
  second entry's model; the body `model` field matches the header.
- **GW-3.AC-3** — With the primary returning 429 and multiple keys in the
  pool, the gateway first rotates keys (mock observes ≥2 distinct keys)
  and only then cascades.
- **GW-3.AC-4** — A 400 from the primary provider is returned to the
  client without any fallback attempt (mock records exactly one upstream
  call).
- **GW-3.AC-5** — With every chain entry failing, the client receives 502
  `upstream_exhausted` and the body enumerates each attempted
  provider/model with a failure class; no provider secret material
  appears in the body.
- **GW-3.AC-6** — With the primary's breaker open (induced by prior
  failures), a new request goes straight to the fallback with zero
  upstream calls to the broken entry.
- **GW-3.AC-7** — A streaming request whose provider dies mid-stream
  yields a terminal error event; the stream never continues with content
  from a different model.
- **GW-3.AC-8** — After an alias edit makes two adjacent entries resolve
  identically, `GET /v1/health` (GW-5) flags the rule degraded and a
  request through it skips the duplicate entry.

## Non-goals

- No load balancing / traffic splitting across chain entries — a chain is
  strictly ordered failover, not weighted routing (a separate future
  requirement if ever needed).
- No automatic response-quality comparison between primary and fallback.
- No client-side retry prescription — what clients may assume is defined
  in GW-7; this section governs gateway behavior only.
- No cross-request stickiness ("keep using the fallback"); breaker state,
  not session affinity, decides where the next request goes.
