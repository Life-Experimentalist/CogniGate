# Changelog

All notable changes to CogniGate are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html) across the whole
deployment — the gateway and the analytics engine ship as one version.

What the version number promises is defined in
[GW-9](spec/gw-09-versioning.md): the API surface is the compatibility contract.
PATCH releases change nothing a client can observe. MINOR releases only add.
Anything that removes a documented endpoint, header, error code, metric, event
type or webhook field, or changes what one means, is a MAJOR release.

Removing something takes three steps, and this file is the first of them. The
release that announces the removal names the element and the date it stops
working, the affected calls carry an `X-CogniGate-Deprecation: <element>;
sunset=<date>` header from that release onward, and the element itself is not
removed before the next MAJOR release and at least six months have passed. A
release note that announces a removal without naming its sunset date fails
conformance (GW-9.AC-6), which is checked in CI against this file.

## [Unreleased]

The first release has not been cut. Everything below is the state of `main`.

### Added

- **Dynamic model discovery (GW-1).** Per-tenant catalogues refreshed from each
  configured provider, served at `GET /v1/models`, with a stale-catalogue
  warning rather than a hard failure when a provider stops answering.
- **Capability aliases (GW-2).** Admin-defined names — `fast`, `best`, whatever
  a tenant wants — that resolve against the live catalogue, so a new model
  reaches clients without a client change.
- **Fallback chains (GW-3).** Ordered per-tenant cascades with a circuit breaker
  per provider and model, a bounded depth, and `X-CogniGate-Fallback-Depth` on
  the response so a caller can see what it cost.
- **Quota and budget API (GW-4).** Token and spend limits per window, enforced
  or reported depending on configuration, with `X-CogniGate-Quota-State` and a
  `quota_exceeded` rejection when enforcement is on.
- **Health and honest degradation (GW-5).** `GET /v1/health` reports what is
  actually reachable rather than whether the process is up, and events are
  raised when a breaker opens or a catalogue goes stale.
- **Admin and configuration API (GW-6).** `/admin/v1/*` under separate keys with
  scopes, covering tenants, providers, keys, aliases, chains, quotas, webhooks
  and an audit log, with a consistent pagination envelope.
- **Client contract (GW-7).** One error envelope across both planes, a fixed
  error-code registry, request-id propagation, and the `X-CogniGate-*` extension
  headers.
- **Observability (GW-8).** One structured log line per request carrying a fixed
  field list, the specified Prometheus series at `/metrics`, and a per-tenant
  event history at `GET /admin/v1/tenants/{id}/events` that a client can poll
  whether or not a webhook was ever delivered.
- **Versioning and capability discovery (GW-9).** `GET /v1/meta`, served
  identically on the admin plane, reporting the version, the API major, the
  capabilities this deployment implements and has enabled, and the limits it
  enforces.
- **Conformance suite (GW-10).** A black-box suite that runs against a
  deployment over HTTP and emits a machine-readable report, selecting which
  sections to run from what `/v1/meta` claims.
- **Size limits and time budgets (GW-13).** A request body cap, an upstream
  response cap, a total request budget spanning the fallback cascade, a
  stream idle timeout, a per-tenant request rate and a per-key in-flight cap
  — each with its own error code, each narrowable per tenant through the
  `limits` block on `PATCH /admin/v1/tenants/{id}`, and each published in
  `GET /v1/meta` so a client can size its requests against the figure that is
  actually enforced.
- **Response caching (GW-12), off by default.** An opt-in cache for requests a
  provider would answer the same way twice: non-streaming, deterministic as
  expressed, and asked for either by `X-CogniGate-Cache: prefer` or by a tenant
  policy. A hit replays the stored body with `X-CogniGate-Cache: hit`, calls no
  upstream, and meters no tokens and no cost. Keys are a SHA-256 over the tenant
  id, the *resolved* provider and model, and the canonicalized request body, so
  entries are never shared between tenants and an alias repin misses rather than
  serving the model it no longer points at. `POST
  /admin/v1/tenants/{id}/cache/flush` clears a tenant's entries.

  Two things about it are worth knowing before turning it on. The cache lives in
  the gateway process rather than in Redis, as
  [GW-12](spec/gw-12-response-caching.md) explains, so a multi-replica
  deployment caches per replica: the hit rate is lower than a shared cache
  would give, and a flush clears the replica that serves it. And the per-alias
  `cache` policy the specification describes is not implemented — the switch is
  per tenant and per request.

- **Content-blind design, with an opt-in debug capture (GW-14).** No prompt and
  no completion reaches anything the deployment keeps. Log lines, metric labels,
  events, webhook deliveries, usage records and everything sent to the analytics
  engine carry counts, identifiers and outcomes — never message text — and the
  conformance suite plants a sentinel string in a request and then looks for it
  across all of them.

  One deliberate exception exists, off until an administrator turns it on. The
  `debug_capture` block on `PATCH /admin/v1/tenants/{id}` retains a sampled
  fraction of one tenant's request and response bodies for a bounded time,
  readable at `GET /admin/v1/tenants/{id}/captures` and nowhere else. While it
  is on, every data-plane response for that tenant carries
  `X-CogniGate-Debug-Capture: on`, so a client can tell it is being recorded
  without having to ask whoever administers the deployment. Turning it on is
  written to the audit log and raises a `debug_capture.enabled` event, a TTL
  above the deployment ceiling is refused with `capture_ttl_too_long` rather
  than quietly clamped, and `sample_rate: 1.0` is accepted with a warning that
  says what it means.

  Captures live in the gateway process, as the response cache does and for the
  same reason [GW-14](spec/gw-14-privacy.md) gives: the gateway has no database.
  A restart drops them, each replica captures only the traffic it served, and
  the bodies come back base64-encoded because they are bytes the gateway never
  parsed. A streamed response is captured as its request alone — reading a
  stream's body would consume it, and a capture feature that changed how the
  gateway served the request it was capturing would be worse than not having one.
