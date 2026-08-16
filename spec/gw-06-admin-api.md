# GW-6 — Admin/config API

**Status:** Specified · **Plane:** Admin · **Depends on:** GW-1..GW-4

## Motivation

Downstream products want to ship their *own* admin consoles (an internal
operations screen, a per-customer billing page) on top of CogniGate. That
is impossible if configuration lives in YAML files edited over SSH. Everything an
operator can configure MUST be reachable as an authenticated HTTP API, so
config files remain a bootstrap convenience, never the only interface.

## Behavioral requirements

### Two planes, two credentials

- **Data plane** (`/v1/*`): authenticated with per-tenant `cg-*` bearer
  keys. Data-plane keys MUST NOT be able to read or mutate configuration
  (beyond the self-inspection endpoints `GET /v1/models`, `/v1/usage`,
  `/v1/health`, `/v1/meta`).
- **Admin plane** (`/admin/v1/*`): authenticated with `cga-*` bearer
  keys — a distinct key format, minted at bootstrap (printed once by
  setup, or via `ADMIN_BOOTSTRAP_KEY` env) and manageable thereafter via
  the API itself.
- Admin keys carry a **scope**: `root` (all tenants) or
  `tenant:<id>` (that tenant's own sub-tree only — so a downstream
  product can embed a tenant-scoped admin key in its per-customer
  console without exposing neighbors). A `tenant:` scoped key MUST
  receive 404 (not 403) for objects outside its tenant, to avoid
  existence leaks.
- Presenting a `cg-*` key on `/admin/v1/*`, or a `cga-*` key on data
  endpoints, MUST fail with 401 `error.code = "wrong_plane"`.
- Deployments MAY additionally bind the admin plane to a separate
  listener/port for network isolation; the path split is the normative
  contract.

### Resources

Full CRUD (JSON bodies, standard verbs — `GET` list/read, `POST` create,
`PATCH` partial update, `DELETE`) for:

| Resource | Path | Notes |
| -------- | ---- | ----- |
| Tenants | `/admin/v1/tenants` | create returns tenant id; delete requires `?confirm=<id>` |
| API keys (data plane) | `/admin/v1/tenants/{id}/keys` | create returns the `cg-*` secret **once**; thereafter only prefix + metadata; revocation is immediate (≤10 s propagation) |
| Admin keys | `/admin/v1/admin-keys` | root scope only; same show-once rule |
| Providers | `/admin/v1/tenants/{id}/providers` | upstream name, kind, base URL, and a write-only key pool: secrets are accepted on write and never returned, reads yield prefixes + status |
| Routing rules | `/admin/v1/tenants/{id}/routing-rules` | validated per GW-3 at save time |
| Aliases | `/admin/v1/tenants/{id}/aliases` | validated per GW-2 at save time |
| Quota | `/admin/v1/tenants/{id}/quota` and `.../keys/{kid}/quota` | per GW-4; one quota object per subject, so both are singletons |
| Usage (read) | `/admin/v1/tenants/{id}/usage[...]` | mirror of GW-4 query API |
| Catalog refresh | `POST /admin/v1/catalog/refresh` | per GW-1 |
| Webhooks | `/admin/v1/tenants/{id}/webhooks` | per GW-8 |

- List endpoints MUST paginate (`?limit=` default 50 max 200, `?after=`
  cursor) and return `{"object":"list","data":[...],"has_more":bool}`.
- Validation failures return 400/409 with the shared error envelope and
  a machine-readable `error.code`; save-time validation for rules and
  aliases is exactly the GW-2/GW-3 rules.
- Secrets — data-plane keys, admin keys, upstream provider keys — MUST
  never be readable back through the API in any form longer than their
  prefix. **Encryption at rest is a property of the store**, not of this
  API: a deployment backed by a durable store MUST encrypt them there
  (the reference persistence layer uses AES-256-GCM); the gateway's
  built-in in-memory store holds them only for the life of the process.
- Every mutation MUST take effect on the data plane within **10
  seconds** without restart, by whatever means the deployment's store
  propagates — the deadline is the contract, the mechanism is not. It
  MUST also be recorded in an append-only admin audit log (actor key
  prefix, action, resource, timestamp, outcome) readable at
  `GET /admin/v1/audit` (root scope). Refused mutations are recorded
  too: an attempt to reach another tenant is precisely what this log is
  read to find.
- The audit log MUST NOT contain request bodies. A provider registration
  carries plaintext upstream credentials, and GW-14 governs this store
  like every other.
- Mutations SHOULD honor idempotency: a repeated `POST` with the same
  `Idempotency-Key` header within 24 h returns the original result.
  *Not implemented in this revision — no acceptance criterion covers it,
  and no conformance test asserts it.*

## Configuration surface

| Key                          | Default | Meaning |
| ---------------------------- | ------- | ------- |
| `ADMIN_BOOTSTRAP_KEY`        | unset   | If set, accepted as a root `cga-*` key at boot (dev/bootstrap) |
| `admin.listen`               | same listener, `/admin/v1` path | Optional separate bind address. *Not implemented — the path split is the normative contract and is what conformance tests.* |

Audit retention is the store's concern, not this API's. A durable store
SHOULD retain entries for at least a year; the built-in in-memory store
bounds the log at its most recent 5,000 entries, because a process that
never restarts would otherwise grow one unboundedly.

File-based config (`cognigate.config.yml`) remains valid as a *seed*: it
is applied at boot as if issued through the API, then the API is the
source of truth.

## Acceptance criteria

- **GW-6.AC-1** — Every configuration used by GW-1..GW-4 (tenant, key,
  provider key, rule, alias, quota) can be created, read (secrets
  excepted), updated, and deleted via `/admin/v1/*` with a root `cga-*`
  key; no step requires editing a file or restarting a process.
- **GW-6.AC-2** — A `cg-*` key on any `/admin/v1/*` endpoint gets 401
  `wrong_plane`; a `cga-*` key on `POST /v1/chat/completions` gets 401
  `wrong_plane`.
- **GW-6.AC-3** — A `tenant:A`-scoped admin key can CRUD tenant A's
  aliases but receives 404 on `/admin/v1/tenants/B/...`.
- **GW-6.AC-4** — Creating a data-plane key returns the full `cg-*`
  secret exactly once; subsequent reads return only its prefix; a request
  with a revoked key fails with 401 within 10 seconds of revocation.
- **GW-6.AC-5** — A stored provider key is never returned by any read
  endpoint in any form longer than its prefix — not by the provider
  read, not by the audit log, not in an error message.
- **GW-6.AC-6** — A routing-rule save violating GW-3's different-model
  rule is rejected here with 400 `fallback_duplicate_model` (same code as
  GW-3.AC-1 — one validator, two entry points).
- **GW-6.AC-7** — A quota `PATCH` is observable in data-plane enforcement
  within 10 seconds (shared with GW-4.AC-6).
- **GW-6.AC-8** — Every mutation in this suite appears in
  `GET /admin/v1/audit` with actor, action, and resource.

## Non-goals

- No bundled admin UI — the API is the product; consoles are downstream
  work. (A minimal reference UI MAY ship, but conformance never tests
  it.)
- No fine-grained per-resource RBAC beyond `root` / `tenant:<id>` scopes
  in this revision.
- No SSO/OIDC on the admin plane in this revision; bearer keys only.
  (Deployments needing SSO front the admin plane with their own proxy.)
- No config-file hot-reload; files seed, the API rules.
