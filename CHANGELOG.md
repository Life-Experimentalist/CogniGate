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

- **What a request cost and what it is charged are now two figures.**
  Every usage record and every usage response carries `charge_usd` beside
  `cost_usd`, so the margin between what the operator paid the provider and
  what the tenant owes is a number rather than an assumption. One new config
  section decides the second figure for the whole gateway:

  ```yaml
  billing:
      mode: passthrough # passthrough | markup | absorb
      markup_pct: 0
      cost_visibility: exact # exact | hint
  ```

  `passthrough` is the default and charges the provider rate itself, so the
  charge follows what the provider charges with no second price table to keep
  current and the operator collects nothing extra. `markup` adds `markup_pct`
  on top, and a `markup` mode with no percentage is refused at startup rather
  than quietly behaving as passthrough. `absorb` charges nothing and leaves the
  whole bill with the operator, with usage still attributed per tenant and per
  key. A model with no published rate costs zero, so it is charged zero in
  every mode: there is nothing to put a margin on.

  Quota caps stay measured in `cost_usd`. A cap on the charge could never fire
  under `absorb`, which is exactly the mode where the operator is the one
  paying. Existing deployments are unaffected: without a `billing` section the
  mode is `passthrough` and `charge_usd` equals `cost_usd` on every row.

  The monthly aggregate in the analytics service now sums the recorded charge
  instead of pricing tokens at a compiled-in flat rate, so the figure it logs
  agrees with what `/v1/usage` returns for the same window.

  `CG_BILLING_MODE`, `CG_BILLING_MARKUP_PCT` and `CG_BILLING_COST_VISIBILITY`
  override the section from the environment, like every other setting.

- **`billing.cost_visibility` decides how much of the cost a tenant sees.**
  A tenant reading its own usage sees both money figures, so under `markup` it
  could divide one by the other and read the margin straight off. `exact`, the
  default, publishes the figure the gateway computed and is the right answer
  under `passthrough`, where there is no margin, and under `absorb`, where teams
  want to know what they are spending. `hint` rounds `cost_usd` to one
  significant figure on the data plane: $1.8734 is published as $2, and dividing
  an exact charge of $2.1544 by that says nothing useful about a markup of 15%.

  The rounding is applied where the response is shaped and nowhere else, so four
  things stay exact under `hint`: `charge_usd`, which is what the tenant owes;
  everything the admin plane returns; the stored row; and quota enforcement,
  which is computed from the real position rather than from what was published.
  A tenant does see a rounded `consumed`, and a `remaining` derived from it
  rather than from the exact number, because a tenant knows its own cap and an
  exact remainder would subtract straight back to the figure the hint blurs.

- **Usage rows record the billing mode, and the admin plane reports the margin.**
  `billing_mode` is stamped on each usage record as it is served, so a window
  read back after the setting changed still says how each request in it was
  priced instead of repricing history. `GET /admin/v1/tenants/{id}/usage` adds
  `margin_usd`, which is `charge_usd` minus `cost_usd`, and it exists on that
  plane alone: what a tenant owes is its own business, what the operator kept on
  top of the provider rate is not. The monthly aggregate log line carries the
  same figure.

- **Rate-limit cooldowns from `Retry-After`.** A key that answers `429` with a
  `Retry-After` is parked for exactly that long, so the next request skips it
  instead of spending a round trip rediscovering a limit the provider already
  reported. Both forms of the header are read, a delay in seconds and an
  absolute date. A provider that sends no `Retry-After` rotates as before, and
  a pool whose keys are all inside a window is still tried.

- **Dynamic model discovery (GW-1).** Per-tenant catalogues refreshed from each
  configured provider, served at `GET /v1/models`, with a stale-catalogue
  warning rather than a hard failure when a provider stops answering.
- **Capability aliases (GW-2).** Admin-defined names — `fast`, `best`, whatever
  a tenant wants — that resolve against the live catalogue, so a new model
  reaches clients without a client change.
- **Fallback chains (GW-3).** Ordered per-tenant cascades with a circuit breaker
  per provider and model, a bounded depth, and `X-CogniGate-Fallback-Depth` on
  the response so a caller can see what it cost.
- **Gemini and Anthropic provider kinds.** `kind: gemini` and `kind: anthropic`
  register against each vendor's OpenAI-compatible endpoint, which they carry as
  a default so `base_url` may be omitted. Both are the existing OpenAI adapter
  under the vendor's own name, so logs, metrics and `X-CogniGate-Served-By`
  attribute the traffic correctly. Nothing translates to Anthropic's Messages
  API or Google's `generateContent`, so native-only features stay out of reach.
- **Key rotation within a provider (`key_strategy`).** A provider's `keys` pool
  is walked round robin by default, one key forward per request, so several
  keys from the same vendor share a steady load instead of piling onto the
  first. `failover` keeps the older behaviour of always starting at key one.
  Under either, a 429 walks the rest of the pool before GW-3 cascades to
  another provider.
- **Operator-set model prices (`catalog.prices`).** A rate is normally carried by
  the provider's own `/models` listing, which Gemini's and Anthropic's do not
  publish. Rates configured here, keyed by provider kind and then model id, price
  those models so `cost_usd` and cost quotas mean something for them. Nothing is
  built in: a model with no entry still costs zero rather than a figure the
  gateway guessed.
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
  field list, a Prometheus scrape at `/metrics` on **both** processes — the
  gateway's specified series, and the analytics engine's own registry — and a
  per-tenant
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

- **A machine-readable contract covering both planes.** `openapi.yaml` now
  describes all 47 operations the gateway serves rather than the single endpoint
  it carried before, and `postman_collection.json` is derived from it. Neither
  is written by hand: `scripts/gen_openapi.py` builds the specification from the
  Go types the gateway actually serialises, and `scripts/gen_postman.py` builds
  the collection from that specification, so the two cannot disagree with each
  other. What keeps them from disagreeing with the gateway is a test rather than
  a habit — the path set is reconciled against the live router in both
  directions, so a route added without an entry fails the build, and so does an
  entry describing a route that no longer exists. The second direction is the
  one that decays quietly: a new route is easy to notice, a stale entry is not.

### Removed

No release has been cut, so nothing below breaks a contract with anyone. These
are corrections to what `main` documented but never implemented, recorded here
because the documentation was public and someone may have planned against it.

- **The plugin engine.** Earlier documentation described uploading `.java`
  source for Janino to compile in memory, and a no-code JSON mapper posted to
  `/api/admin/plugins/upload`. Neither existed. The gateway ships one adapter,
  `openai`, which covers every provider that reimplements the OpenAI wire
  format — Together, Groq, Fireworks, Azure OpenAI, OpenRouter, vLLM, Ollama
  — because only the base URL differs between them. A provider with its own
  protocol needs a translating proxy in front of it. The `/docs/plugins` page
  is gone rather than left describing a feature that was never built, and
  `ai_agent_instructions.md` — a root-level brief telling an AI agent to
  implement `com.cognigate.plugin.AiProviderHandler` against Janino, an
  interface no source file in this repository declares — went with it.
- **Redis.** The same documentation described a Redis 7 fast-path cache and a
  `cognigate:cache:invalidate` Pub/Sub channel. The gateway holds its
  configuration in process memory; the deployment is three containers, not
  four. The consequences are documented rather than hidden: a restart loses
  configuration, and replicas do not share it.
- **`ENCRYPTION_MASTER_KEY` and the AES-256-GCM key vault.** Provider keys are
  held in memory, returned by no route, written to no disk and printed in no
  log line, so there is nothing at rest to encrypt. The variable is gone from
  `.env.example` and from both setup scripts, which now generate the
  `ANALYTICS_TOKEN` the analytics service actually requires.
- **Kubernetes deployment guidance.** No manifests or charts exist. The
  deployment documentation covers the compose reference and says what running
  more than one gateway would take.
- **The contents of the shipped Postman collection.** Four of its five requests
  addressed `http://localhost:8081/api/admin/*`, a port and path prefix this
  gateway has never served — the upload to the plugin engine removed above was
  one of them. The fifth reached a route that does exist,
  `POST /v1/chat/completions`, with a placeholder bearer token no deployment
  would accept. It has been replaced wholesale by one generated from
  `openapi.yaml`. The
  collection id is carried over deliberately, so importing the new file updates
  an existing copy in a Postman workspace instead of appearing beside it — the
  old requests should not survive anywhere as a second collection.

### Fixed

- **`GET /v1/health` and `GET /v1/meta` no longer spend the tenant's rate-limit
  budget.** Both sat behind the per-tenant limiter, so a monitoring poll
  competed with real traffic and a tenant that had just exhausted its budget got
  429 from the two endpoints that exist to tell it what the gateway is doing —
  the endpoints went dark in the situation they are for. Both are now outside
  `rate_limit`. Nothing else about them changed: both still require a valid
  data-plane key, still count against `max_concurrent_per_key`, and are still
  answered from gateway-local state without dialling a provider. This also
  resolves a contradiction in the specification, which required 100 sequential
  calls to each to complete while metering them against a burst of 100.

- **The `LICENSE` file was a paraphrase, not the Apache License 2.0.** Every
  source header, the README badge and `pom.xml` named Apache-2.0, but the file
  itself was a restatement written in the licence's shape — the section
  headings in the right order, each term reworded. A paraphrase grants nothing:
  the permission comes from the exact wording, so the repository was public
  with no operative grant behind the Apache-2.0 claim. The file is now the
  verbatim text, appendix included, with the copyright line filled in as the
  appendix directs. GitHub classifies the repository as Apache-2.0 rather than
  "Other". Anyone who took a copy before this should re-pull — what they hold
  does not grant what it says it does.

### Security

- **The gateway image upgrades its Alpine packages before it installs any.**
  A base image pinned to a release tag ships whatever was current when that
  image was built, which lags the repository the patches land in: the pinned
  base carried an openssl with two critical and seven high advisories that the
  repository had already fixed. Upgrading first takes them, and the published
  image now scans clean.

- **Tomcat pinned to 11.0.25.** Spring Boot 4.1.1 manages 11.0.24, which
  carries three critical advisories, and the parent has not moved yet.
  Overriding the managed `tomcat.version` takes the patch without changing the
  Boot version CI builds against. The override comes out when the parent
  passes 11.0.25.

- **Docker Scout reports on every published image.** The publish workflow scans
  each image by the digest it just pushed and writes critical and high findings
  with a known fix to the run summary. It does not fail the publish: nearly
  every finding lives in a base image, and refusing to ship over a CVE this
  repository cannot patch would stop releases for something a rebuild will not
  fix. The report is the point.

- **Fiber updated from v2.52.4 to v2.52.12, closing two advisories the gateway
  could actually reach.** `GO-2026-4543` is a denial of service through
  route-parameter overflow, reachable from the router itself, and
  `GO-2025-3845` is an unvalidated slice index in `BodyParser`, reachable from
  every admin endpoint that parses a body. Both were reported by the new
  vulnerability scan below, on its first run, before it was committed — which
  is the argument for having it.

- **Dependencies are now watched rather than remembered.**
  `.github/dependabot.yml` covers all five surfaces: both Go modules, the
  Maven module, the docs site's npm tree, the workflows' own actions, and the
  container base images. The Go module block allows indirect dependencies,
  because `go.mod` records the whole transitive graph and watching only direct
  requirements lets the other half drift while every run stays green. The two
  builder base images are ignored outright: they are toolchain pins that have
  to agree with `go.mod` and the pom, they are discarded before the published
  image is assembled, and Dependabot cannot reliably tell how big a change to
  one of them is — its first run proposed `maven:3.9-eclipse-temurin-25` →
  `-temurin-26`, a whole JDK major, because the part it compares is the
  unchanged leading `3.9`. The runtime images update freely, since they carry
  the OS packages and cannot change how anything was compiled.

- **CI scans both Go modules against the Go vulnerability database on every
  push,** with `govulncheck` invoked directly rather than through a wrapper
  action. It reports only what the code can reach, so it is a complement to
  Dependabot and not a replacement.

- **CodeQL analyses the Go source, the Java source and the workflows,** on
  every push and pull request and on a weekly schedule. The schedule is the
  part that matters for a project that goes quiet: it re-runs settled code
  against queries that did not exist when the code was written.

- **Published images and release artifacts now carry signed build provenance,
  and the images carry an SPDX SBOM.** The provenance is a statement, signed
  against a short-lived OIDC certificate, naming this repository's workflow at
  a specific commit as what produced a given digest. It is attached to the
  digest rather than to a tag, because a tag is a movable pointer. Anyone can
  check one without cloning anything:

  ```bash
  gh attestation verify oci://ghcr.io/life-experimentalist/cognigate-gateway:main --repo Life-Experimentalist/CogniGate
  ```

  Release binaries are attested the same way; the command is printed in each
  release's notes. No release has been cut yet, so that half is configured and
  not yet exercised.
