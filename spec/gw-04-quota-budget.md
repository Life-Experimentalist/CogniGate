# GW-4 — Quota & budget API

**Status:** Specified · **Plane:** Data + admin · **Depends on:** metering (Phase 1)

## Motivation

Metering that only records is bookkeeping; consumers need *enforcement*
(a hospital, a startup, a lab all need "this tenant cannot spend more
than X this month") and *visibility* (their admin dashboards must show
spend without scraping CogniGate logs). Quotas are the difference between
a router and infrastructure someone can hand a budget to.

## Behavioral requirements

### Quota model

- Quotas are defined **per tenant**, and optionally **per API key**
  (a key-level quota further constrains, never extends, the tenant's).
- Each quota has independent limits over two windows — **day** (UTC
  calendar day) and **month** (UTC calendar month) — in two units:
  - `tokens` (prompt + completion, as metered)
  - `cost` (USD, decimal string, computed from metering's price table)
- Each limit carries a **soft threshold** (percentage, default 80) and a
  **hard cap**. Any of the four limit slots (day/month × tokens/cost) MAY
  be unset (unlimited).

### Enforcement

- Enforcement happens at request admission on the data plane, before any
  upstream call.
- Over the **hard cap**: the request is rejected with HTTP 429 and a
  distinct, documented error code —
  `error.code = "quota_exceeded"` (token limits) or
  `error.code = "budget_exceeded"` (cost limits) — plus:
  - `Retry-After: <seconds until the window resets>`
  - `X-CogniGate-Quota-State: hard-exceeded`
- Between **soft threshold** and hard cap: the request proceeds, with
  `X-CogniGate-Quota-State: soft-exceeded` on the response, and a
  `quota.threshold_crossed` event fires **once per window crossing**
  (GW-8), not per request.
- Under the soft threshold: `X-CogniGate-Quota-State: ok`.
- Enforcement uses metered usage as of admission; the in-flight request
  that crosses the cap completes. Overshoot by at most one request per
  key is accepted and documented — CogniGate does not pre-reserve tokens.
- Quota changes via GW-6 MUST take effect on the data plane within 10
  seconds (cache invalidation via the existing Redis
  `cognigate:cache:invalidate` channel), without restart.

### Query API

Read endpoints live on the data plane (a tenant may inspect itself) and
the admin plane (any tenant):

- `GET /v1/usage` (data plane, `cg-*` key) MUST return, as JSON, for each
  active window: consumed tokens, consumed cost, configured caps and
  thresholds, remaining amounts, `resets_at` (RFC 3339), and current
  state (`ok | soft-exceeded | hard-exceeded`).
- `GET /v1/usage/breakdown?window=day|month&group_by=model|provider|key`
  MUST return per-group consumed tokens and cost for the window. This is
  the endpoint a downstream admin dashboard renders directly — it MUST
  be servable without log access.
- Admin-plane equivalents: `GET /admin/v1/tenants/{id}/usage[...]`
  (GW-6).
- Usage figures MUST be no more than 60 seconds stale relative to
  completed requests.

## Configuration surface

| Key                                       | Default | Meaning |
| ----------------------------------------- | ------- | ------- |
| `quotas.default_soft_threshold_pct`       | `80`    | Applied when a quota omits its own |
| `quotas.enforcement`                      | `on`    | `on \| observe` (observe = headers/events only, never 429) |
| Per-tenant / per-key quota objects        | unset   | Managed via GW-6 |
| Price table for cost computation          | adapter-supplied | Overridable per deployment via admin plane |

## Acceptance criteria

- **GW-4.AC-1** — With a tenant day-token cap of 1000 and 1000 tokens
  already metered, `POST /v1/chat/completions` returns 429 with
  `error.code = "quota_exceeded"`, a `Retry-After` header, and
  `X-CogniGate-Quota-State: hard-exceeded`; the mock provider records
  zero upstream calls for it.
- **GW-4.AC-2** — Crossing the soft threshold flips response headers to
  `soft-exceeded` while requests still return 200, and exactly one
  `quota.threshold_crossed` event is emitted for the window.
- **GW-4.AC-3** — A key-level cap smaller than the tenant cap blocks that
  key at its own limit while a sibling key continues to work.
- **GW-4.AC-4** — `GET /v1/usage` reflects a just-completed request's
  tokens within 60 seconds, and its `remaining` plus `consumed` equals
  the configured cap.
- **GW-4.AC-5** — `GET /v1/usage/breakdown?window=day&group_by=model`
  sums to the same totals as `GET /v1/usage` for the same window.
- **GW-4.AC-6** — Raising the cap via the admin plane unblocks a
  previously 429'd key within 10 seconds without any restart.
- **GW-4.AC-7** — Cost-capped tenants get `budget_exceeded` (not
  `quota_exceeded`) when the cost cap trips; the two codes are
  distinguishable by a client.
- **GW-4.AC-8** — With `quotas.enforcement: observe`, over-cap requests
  succeed but carry `hard-exceeded` headers (dry-run mode for adopters).

## Non-goals

- No billing/invoicing UI — CogniGate exposes numbers; presenting or
  charging them is the consumer's business. (The existing invoice cron
  is unaffected.)
- No pre-reservation or exact atomic budget accounting across concurrent
  requests; the one-request overshoot is accepted by design.
- No per-end-user quotas (CogniGate's unit is tenant/key; downstream apps
  map their own users onto keys if they need finer granularity).
- No currency conversion; cost is USD as priced by the deployment's
  price table.
