# GW-7 — Client/SDK contract

**Status:** Specified · **Plane:** Data · **Depends on:** GW-1..GW-5

## Motivation

"OpenAI-compatible" is a promise clients build on; it needs edges. This
section defines exactly what a downstream client must implement, what it
may rely on, and what it must never do — so any team can write a thin
CogniGate adapter (or point a stock OpenAI SDK at the gateway) without
reading CogniGate's source.

## Behavioral requirements

### What a client implements

- **Base URL + bearer key.** All data-plane traffic goes to the
  deployment's base URL with `Authorization: Bearer cg-<key>`. Stock
  OpenAI SDKs work by setting `base_url` and `api_key`; no other client
  change is required.
- **Endpoints CogniGate serves** (the compatibility surface; the live
  list is feature-detectable via `/v1/meta`, GW-9):

  | Endpoint | Status |
  | -------- | ------ |
  | `POST /v1/chat/completions` (incl. `stream: true` SSE) | REQUIRED |
  | `GET /v1/models`, `GET /v1/models/{id}` | REQUIRED (GW-1) |
  | `POST /v1/embeddings` | OPTIONAL, advertised via meta |
  | `POST /v1/audio/transcriptions` | OPTIONAL, advertised via meta |
  | CogniGate extensions: `GET /v1/usage`, `GET /v1/health`, `GET /v1/meta` | REQUIRED (GW-4/GW-5/GW-9) |

  Requests to unimplemented OpenAI endpoints return 404
  `error.code = "not_supported"` — never a silent proxy pass-through.
- **Intent via aliases.** Clients MUST express model intent via aliases
  (GW-2) and MUST NOT hardcode concrete model ids. Rule of thumb a code
  review can apply: a provider model id appearing as a string literal in
  downstream application code is a contract violation; alias names and
  ids fetched at runtime from `GET /v1/models` are fine.

### What CogniGate adds to every response

Extension headers, present on every data-plane response (success or
error) unless noted:

| Header | Meaning |
| ------ | ------- |
| `X-CogniGate-Request-Id` | Gateway-assigned id (UUID), also in error bodies and logs (GW-8) |
| `X-CogniGate-Served-By` | `<provider>/<model-id>` that actually served (success only) |
| `X-CogniGate-Fallback-Depth` | `0`..N (success only, GW-3) |
| `X-CogniGate-Quota-State` | `ok \| soft-exceeded \| hard-exceeded` (GW-4) |
| `X-CogniGate-Cache` | `hit \| miss \| bypass` (only when caching enabled, GW-12) |

Clients MAY log these; they MUST NOT need them for correct operation.
A client-supplied `X-Client-Request-Id` header (≤128 chars) is echoed
back verbatim and attached to logs/usage records for correlation.

### Error contract

- Every error is the shared envelope (see the index):
  `{"error": {"message", "type", "code", "param"}}` — OpenAI-shaped, so
  stock SDK error handling works, with `code` machine-readable per the
  registry in the index.
- Streaming errors after headers are sent arrive as a terminal SSE event
  `data: {"error": {...}}` followed by stream close.

### Retry semantics clients may rely on

- CogniGate has **already** rotated keys and walked the fallback chain
  (GW-3) before the client sees an error. Therefore:
  - On 5xx / `upstream_exhausted`: clients MAY retry with capped
    exponential backoff (suggested: 3 attempts, 1 s base, jitter), and
    SHOULD surface degradation via GW-5 rather than retrying tightly.
  - On 429 `quota_exceeded` / `budget_exceeded`: clients MUST NOT retry
    before `Retry-After`; this is a policy stop, not a transient fault.
  - On 4xx (except 429): clients MUST NOT retry an identical request.
- Non-streaming `POST /v1/chat/completions` is safe to retry only in the
  sense above; CogniGate does not deduplicate completions — a retry may
  bill twice. Clients that cannot tolerate that use GW-12 caching or
  their own dedup.

### Timeouts

- Clients SHOULD set their HTTP timeout ≥ the deployment's total request
  budget (GW-13 default 120 s) and rely on the gateway, not their own
  timeout, to bound upstream time.

## Configuration surface

None on the gateway beyond what other sections define; this section
constrains clients. A reference client checklist SHOULD ship in docs.

## Acceptance criteria

- **GW-7.AC-1** — An unmodified official OpenAI SDK pointed at the
  gateway (`base_url`, `api_key = cg-*`) completes a non-streaming and a
  streaming chat completion against an alias model name.
- **GW-7.AC-2** — Every data-plane response (200, 4xx, 5xx) carries
  `X-CogniGate-Request-Id`, and the same id appears in the error body's
  envelope for errors.
- **GW-7.AC-3** — A request with `X-Client-Request-Id: abc123` gets the
  same value echoed in the response and stored on the usage record.
- **GW-7.AC-4** — `GET /v1/nonexistent-openai-endpoint` returns 404
  `not_supported` with the standard envelope.
- **GW-7.AC-5** — All error responses parse with a stock OpenAI SDK's
  error class (envelope shape check across the registry's codes).
- **GW-7.AC-6** — 429 quota errors carry `Retry-After`; 400 responses do
  not invite retry (no `Retry-After` present).

## Non-goals

- CogniGate ships no official client SDKs; the contract is the SDK.
- No perfect bug-for-bug OpenAI parity — unsupported endpoints fail
  loudly (`not_supported`) instead of being emulated badly.
- No request signing / mTLS on the data plane in this revision (TLS
  termination options are GW-11).
- No client-side alias fallback logic — resolution and failover are
  gateway concerns; a client implementing its own model failover is
  fighting GW-3.
