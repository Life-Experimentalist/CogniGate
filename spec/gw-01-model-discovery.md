# GW-1 — Dynamic model discovery

**Status:** Specified · **Plane:** Data + internal poller · **Depends on:** —

## Motivation

Provider model lineups change weekly. Any static model list — in CogniGate
config, in a client, in a downstream admin UI — rots silently and produces
runtime 404s or, worse, silent routing to a deprecated model. The catalog
must therefore be discovered from the providers themselves, cached, and
served per-tenant, so that neither CogniGate operators nor downstream
applications ever hand-maintain a model list.

## Behavioral requirements

### Discovery

- CogniGate MUST poll each configured provider's own model-listing API
  (`GET /models` on OpenAI-compatible providers; the equivalent endpoints
  on Anthropic and Gemini adapters) on a TTL cache.
- The TTL MUST be configurable per deployment; the default is **1 hour**.
- An on-demand refresh MUST be triggerable via the admin plane
  (`POST /admin/v1/catalog/refresh`, see GW-6). Refresh MUST be
  rate-limited internally (at most one in-flight refresh per provider).
- A failed poll (provider down, key invalid) MUST NOT evict the previous
  catalog snapshot for that provider. The stale snapshot is retained and
  its age is reported via GW-5 (`catalog.age_seconds`) and GW-8 metrics.
- Discovery MUST use a provider key from the tenant-agnostic pool for that
  provider; if only per-tenant keys exist, discovery runs per tenant and
  results are scoped accordingly.

### Serving the catalog

- `GET /v1/models` (data plane, `cg-*` key) MUST return the union of
  models available **to the calling tenant right now**: only providers the
  tenant has credentials or routing rules for, minus models excluded by
  tenant policy.
- The response MUST be OpenAI-list-shaped (`{"object":"list","data":[...]}`)
  so stock OpenAI SDKs parse it. Each entry MUST carry `id`, `object:
  "model"`, `owned_by` (provider slug), and CogniGate extension fields
  under a `cognigate` key:

  ```json
  {
    "id": "gpt-4o-mini",
    "object": "model",
    "owned_by": "openai",
    "cognigate": {
      "provider": "openai",
      "context_window": 128000,
      "modalities": ["text"],
      "capabilities": ["chat", "tools", "json_mode"],
      "discovered_at": "2026-09-01T10:00:00Z"
    }
  }
  ```

  Metadata fields MAY be `null` where the provider does not expose them;
  they MUST NOT be guessed.
- `GET /v1/models/{id}` MUST return the single entry or a 404 with
  `error.code = "model_not_found"`.
- Tenant aliases (GW-2) MUST also appear in `GET /v1/models` as entries
  with `"cognigate": {"alias": true, "resolves_to": "<model-id>"}` so
  clients can enumerate what they may put in the `model` field.

### Removal semantics

- A model that disappears upstream MUST drop out of the catalog on the
  next refresh.
- Requests naming a removed model MUST fail over via its routing rule's
  fallback chain (GW-3) when one exists; only when no chain entry can
  serve the request does the gateway return an error
  (`error.code = "model_not_found"`, HTTP 404).
- Removal MUST emit a `catalog.model_removed` event (GW-8) and, when the
  model is referenced by any alias or routing rule, flag that rule as
  degraded in the admin plane (GW-6).

## Configuration surface

| Key (cognigate.config.yml / env)          | Default | Meaning |
| ----------------------------------------- | ------- | ------- |
| `catalog.refresh_ttl` / `CATALOG_TTL`     | `1h`    | Poll interval per provider |
| `catalog.stale_warn_after`                | `6h`    | Age at which GW-5 reports catalog `degraded` |
| `catalog.provider_timeout`                | `10s`   | Per-provider listing call timeout |
| `tenants.<t>.model_denylist`              | `[]`    | Model ids hidden from that tenant |

## Acceptance criteria

- **GW-1.AC-1** — `GET /v1/models` with a valid `cg-*` key returns 200,
  `object == "list"`, and every entry has `id`, `owned_by`, and a
  `cognigate.provider` field.
- **GW-1.AC-2** — `GET /v1/models` with no/invalid key returns 401 with
  `error.code = "invalid_api_key"`.
- **GW-1.AC-3** — Two tenants with different provider configurations
  receive different catalogs; no tenant sees a provider it has no key for.
- **GW-1.AC-4** — After `POST /admin/v1/catalog/refresh`, a model newly
  added at a (mock) provider appears in `GET /v1/models` without a
  gateway restart.
- **GW-1.AC-5** — After a model is removed at a (mock) provider and a
  refresh runs, the model is absent from `GET /v1/models`, and a request
  naming it with a configured fallback chain succeeds via the fallback
  (verify `X-CogniGate-Served-By` names a different model).
- **GW-1.AC-6** — With the (mock) provider's listing endpoint down, the
  previous catalog continues to be served and `GET /v1/health` reports a
  non-zero catalog age for that provider.
- **GW-1.AC-7** — No model id served by `GET /v1/models` appears anywhere
  in CogniGate's committed configuration files (static-list ban;
  conformance suite greps the deployment's config for served ids).

## Non-goals

- CogniGate does not benchmark or rank models; it reports provider
  metadata, it does not editorialize.
- No price catalog is required here — cost data belongs to metering
  (GW-4); where the provider publishes prices the adapter MAY attach
  them, but absence is not a conformance failure.
- No cross-provider model-equivalence mapping ("gpt-4o ≈ claude-…") —
  that is alias policy (GW-2), decided by admins, not inferred.
