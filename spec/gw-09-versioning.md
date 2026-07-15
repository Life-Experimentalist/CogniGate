# GW-9 — Versioning & compatibility

**Status:** Specified · **Plane:** Data + admin · **Depends on:** GW-7

## Motivation

Downstream projects pin their trust to a contract, not a git SHA. They
need to know which CogniGate they are talking to, which capabilities it
implements, and how long a behavior they depend on will keep working —
programmatically, so adapters can feature-detect instead of breaking.

## Behavioral requirements

### Semantic versioning

- CogniGate releases MUST use semver `MAJOR.MINOR.PATCH` for the whole
  deployment (one version covers gateway + analytics; they ship
  together).
- The **API surface** (every documented endpoint, header, error code,
  metric name, event type, and webhook payload field in GW-1..GW-14) is
  the compatibility contract:
  - PATCH: no observable contract change.
  - MINOR: additive only — new endpoints, new optional fields, new error
    codes, new event types. Clients MUST ignore unknown fields/headers/
    event types (tolerant-reader rule, restated from GW-7/GW-8).
  - MAJOR: anything that removes or changes the meaning of an existing
    contract element.
- The URL prefix `/v1` (and `/admin/v1`) tracks MAJOR. A future MAJOR
  serves `/v2` alongside `/v1` for the deprecation window.

### Feature detection: `GET /v1/meta`

- Authenticated with any valid data-plane key; also served identically
  at `GET /admin/v1/meta` for admin keys. Response:

  ```json
  {
    "name": "cognigate",
    "version": "1.4.0",
    "api_version": "v1",
    "capabilities": [
      "gw-1", "gw-2", "gw-3", "gw-4", "gw-5",
      "gw-6", "gw-7", "gw-8", "gw-9", "gw-12"
    ],
    "endpoints": ["chat.completions", "models", "usage", "health"],
    "limits": { "max_request_bytes": 2097152, "max_fallback_depth": 5 }
  }
  ```

- `capabilities` lists the requirement IDs of this spec that the
  deployment implements and has enabled. A capability listed here MUST
  pass its conformance section (GW-10); listing an unimplemented
  capability is itself a conformance failure.
- Optional features that are configured off (e.g. GW-12 caching
  disabled) MUST be absent from `capabilities`, so clients detect the
  live truth, not the binary's potential.
- The response MUST be stable within a process lifetime and cheap
  (served from memory).

### Deprecation policy

- Removing or changing any contract element requires: (a) a deprecation
  notice in the release notes at MINOR release N, (b) the response
  header `X-CogniGate-Deprecation: <element>; sunset=<RFC 3339 date>` on
  affected calls from N onward, and (c) removal no sooner than the
  **next MAJOR and 6 months** after the notice.
- Deprecated-but-working elements MUST keep passing conformance until
  removal.

## Configuration surface

None. Version and capability reporting are not configurable — that is
the point.

## Acceptance criteria

- **GW-9.AC-1** — `GET /v1/meta` with a valid `cg-*` key returns 200 with
  `version` (valid semver), `api_version = "v1"`, and a `capabilities`
  array of `gw-*` ids.
- **GW-9.AC-2** — For every id in `capabilities`, the corresponding
  conformance section (GW-10) passes; for every id absent, the suite
  skips it rather than failing (drives suite selection).
- **GW-9.AC-3** — `limits` in meta match observed enforcement: a request
  of `max_request_bytes + 1` is rejected per GW-13.
- **GW-9.AC-4** — With an optional capability disabled by config
  (e.g. caching off), its id is absent from `capabilities` and its
  headers (`X-CogniGate-Cache`) never appear.
- **GW-9.AC-5** — `GET /v1/meta` p99 latency < 50 ms over 100 calls.
- **GW-9.AC-6** — (Release-process check, asserted in CI rather than
  against a live deployment) the changelog for any release that adds a
  `X-CogniGate-Deprecation` header names the element and its sunset
  date.

## Non-goals

- No multi-version simultaneous support within a MAJOR (there is one
  `/v1`; MINORs are additive by definition).
- No client version negotiation (`Accept-Version` etc.) — capabilities,
  not negotiation.
- No stability promise for undocumented behavior; anything not written
  in GW-1..GW-14 or the OpenAPI document may change in a PATCH.
- Internal schemas (Postgres tables, Redis key layout) are explicitly
  outside the contract; only the HTTP surface is versioned.
