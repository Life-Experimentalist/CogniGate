# GW-12 — Response caching for deterministic requests (optional)

**Status:** Specified (OPTIONAL capability) · **Plane:** Data + admin · **Depends on:** GW-7, GW-9

## Motivation

Downstream products run many *deterministic* AI tasks — classification,
extraction, canned transformations — where an identical request warrants
an identical answer. Serving those from a gateway cache cuts cost and
latency and removes double-billing on client retries (GW-7). Caching
LLM responses is only safe when it is explicit, scoped, and honest,
hence: off by default, opt-in, and always labeled.

## Behavioral requirements

### Eligibility

- Caching is a deployment capability; when enabled it appears as
  `gw-12` in `GET /v1/meta` capabilities (GW-9). When disabled, the
  `X-CogniGate-Cache` header MUST never appear.
- A response is cacheable only when **all** hold:
  - non-streaming (`stream` absent or false);
  - `temperature` is 0 (or omitted with `top_p` at 1 and `n` at 1 —
    i.e. the request is deterministic as expressed);
  - the request opted in: header `X-CogniGate-Cache: prefer` **or** the
    tenant has a `cache: enabled` policy via GW-6;
  - response status 200.
- `X-CogniGate-Cache: bypass` on a request MUST skip lookup and storage
  regardless of policy.

### Keying & scope

- Cache key = SHA-256 over: tenant id, **resolved** provider+model id
  (post GW-2/GW-3 — so an alias repin naturally misses), and the
  canonicalized JSON request body minus fields that do not affect the
  completion (`user`, `metadata`, `stream_options`).
- Entries are **strictly tenant-scoped**. Cross-tenant sharing is
  forbidden even for byte-identical requests (tenant isolation beats
  hit rate; see GW-14).
- Default TTL 5 minutes, configurable per tenant up to a deployment
  ceiling; storage is a byte-bounded LRU inside the gateway process.
  This is a departure from the sketch this specification was first
  written against, which put the cache in Redis. The gateway's tenants,
  keys, aliases and routes are in-process, so a cache that outlived a
  restart would be keyed on tenant ids that no longer exist. The trade
  is that a multi-replica deployment caches per replica: the hit rate is
  lower, and a flush clears the replica that serves it. Both are stated
  here rather than hidden, because a cache that is wrong is worse than
  no cache, and GW-12 is optional precisely so a deployment can decline
  the trade.

### Serving

- A hit MUST return the stored body byte-identical (fresh
  `X-CogniGate-Request-Id` excepted at the header level; the body,
  including its original `id` and `usage`, is replayed verbatim) with:
  - `X-CogniGate-Cache: hit`
  - `X-CogniGate-Served-By` of the original serving model.
- A hit MUST NOT meter new tokens or cost (GW-4) — the usage record for
  a hit is written with `cached: true` and zero incremental cost — and
  MUST NOT count against provider rate limits (no upstream call).
- A miss serves normally and stores; `X-CogniGate-Cache: miss`.
- Flush: `POST /admin/v1/tenants/{id}/cache/flush` (GW-6) clears a
  tenant's entries within 10 seconds.

## Configuration surface

| Key                          | Default | Meaning |
| ---------------------------- | ------- | ------- |
| `cache.enabled`              | `false` | Master switch (capability flag) |
| `cache.default_ttl`          | `5m`    | Per-entry TTL when policy omits one |
| `cache.max_ttl`              | `24h`   | Ceiling any tenant policy may request |
| `cache.max_entry_bytes`      | `262144` | Responses larger than this are never cached |
| `cache.max_bytes`            | `67108864` | Total the cache may hold; the LRU evicts to stay inside it |
| Per-tenant `cache` policy    | off     | `{enabled, ttl_seconds}`, managed via GW-6 |

Per-*alias* cache policy is specified above but not implemented: no
acceptance criterion exercises it, and an alias-level switch that only
the specification knows about would be a capability claimed and not
delivered. A tenant-level policy plus the per-request header covers
every criterion here.

## Acceptance criteria

- **GW-12.AC-1** — With caching enabled, two identical `temperature: 0`
  requests with `X-CogniGate-Cache: prefer` yield `miss` then `hit`;
  the mock provider records exactly one upstream call; both bodies'
  `choices` are byte-identical.
- **GW-12.AC-2** — The hit does not increase `GET /v1/usage` consumed
  tokens/cost, and its usage record carries `cached: true`.
- **GW-12.AC-3** — The same request with `temperature: 0.7`, or with
  `stream: true`, is never cached (two upstream calls, `bypass`/`miss`
  semantics per the eligibility rules).
- **GW-12.AC-4** — Tenant B issuing tenant A's exact cached request gets
  a miss (tenant scoping).
- **GW-12.AC-5** — After the alias resolution changes to a different
  model, the previously cached request misses (resolved-model in key).
- **GW-12.AC-6** — Admin cache flush causes the next identical request
  to miss within 10 seconds.
- **GW-12.AC-7** — With `cache.enabled: false`, `gw-12` is absent from
  meta capabilities and no response carries `X-CogniGate-Cache`
  (shared with GW-9.AC-4).

## Non-goals

- No semantic/embedding-similarity caching — exact-match only; "close
  enough" caching is a research feature, not infrastructure.
- No caching of streaming responses.
- No cross-tenant or cross-deployment cache sharing.
- No persistent (disk-durable) cache; entries may vanish at any time
  and clients MUST NOT depend on a hit.
- Prompt content in cache entries lives only in the gateway's own
  memory, under the deployment's control, and expires by TTL or with
  the process, whichever comes first; this is distinct from the
  debug retention governed by GW-14, and the GW-14 log/telemetry
  content ban still applies.
