# GW-10 — Conformance test suite

**Status:** Specified · **Plane:** Tooling · **Depends on:** all others

## Motivation

A spec nobody can run is an opinion. Downstream projects need a
one-command way to point a suite at *any* CogniGate deployment — theirs,
staging, a vendor's — and get a pass/fail per requirement. The same
suite is CogniGate's own regression harness, so the spec and the
implementation cannot drift apart silently.

## Behavioral requirements

### Packaging & invocation

- The suite MUST ship in this repository (`conformance/`) and be runnable
  two ways with identical results:
  - `go test ./conformance/...` (it is ordinary Go tests), and
  - a container: `docker run ghcr.io/…/cognigate-conformance:<version>`.
- All target-specific input arrives via environment/flags:

  | Variable | Meaning |
  | -------- | ------- |
  | `CONF_BASE_URL` | Data-plane base URL of the target deployment |
  | `CONF_ADMIN_KEY` | Root `cga-*` key (the suite provisions its own throwaway tenant) |
  | `CONF_MOCK_PROVIDER` | `embedded` (default) or URL of an externally-run mock |

- The suite MUST create its own tenant(s), keys, aliases, rules, and
  quotas via GW-6, run against them, and delete them afterwards — it
  never touches pre-existing tenants, so it is safe to run against a
  shared deployment.
- The suite MUST bundle a **mock OpenAI-compatible provider** (fault
  injection: 429/5xx/timeout/model add-remove on command) and register
  it as a provider via GW-6, because acceptance criteria like GW-3.AC-2
  require controlled upstream failure. Real-provider smoke mode
  (`CONF_REAL_PROVIDER_KEY`) is OPTIONAL and off by default.

### Structure & selection

- Tests are grouped by requirement id; each acceptance criterion in
  GW-1..GW-14 maps to exactly one test whose name embeds the AC id
  (e.g. `TestGW3_AC2_FallbackOnServerError`). The mapping is 1:1 and
  auditable: a script in the repo MUST verify every `AC` listed in
  `spec/` has a matching test, and fail CI when one is missing.
- Selection: `-run 'GW3'` runs one requirement; by default the suite
  reads the target's `GET /v1/meta` capabilities (GW-9) and **skips**
  requirements the deployment does not claim, reporting them as
  `SKIP (not claimed)` — claiming a capability and failing it is a
  failure; not claiming it is not.
- Tests MUST be independent and safe to run in parallel except where an
  AC inherently serializes (breaker manipulation); those declare it.

### Reporting

- Output: standard `go test` output, plus `-json` machine output, plus a
  summary artifact `conformance-report.json`:

  ```json
  {
    "target": "http://localhost:8080",
    "gateway_version": "1.4.0",
    "suite_version": "1.4.0",
    "results": [
      {"id": "GW-3.AC-2", "status": "pass", "duration_ms": 412}
    ],
    "summary": {"pass": 92, "fail": 0, "skip": 8}
  }
  ```

- Exit code 0 iff no failures. JUnit XML output MUST be producible
  (`gotestsum` or equivalent) for CI systems.

### Dual role

- CogniGate's own CI MUST run the full suite against a fresh
  docker-compose deployment on every merge to main; a release (GW-9)
  MUST NOT ship with failures in any claimed capability.
- Suite versioning follows the gateway's semver; a suite at version X
  is authoritative for deployments claiming API version X's contract.

## Configuration surface

The variables above; no gateway-side configuration. A gateway MUST NOT
be able to detect "conformance mode" — the suite exercises only public
planes.

## Acceptance criteria

(Meta-criteria — asserted about the suite itself, in this repo's CI.)

- **GW-10.AC-1** — `go test ./conformance/...` against the reference
  docker-compose deployment passes with zero failures.
- **GW-10.AC-2** — The AC-coverage script confirms a 1:1 mapping between
  every `GW-*.AC-*` id in `spec/` and a test; CI fails on a gap.
- **GW-10.AC-3** — Running the suite twice concurrently against the same
  deployment passes both times (tenant isolation of the suite itself).
- **GW-10.AC-4** — After the suite completes, no suite-created tenant,
  key, or webhook remains (verified via GW-6 list endpoints).
- **GW-10.AC-5** — Against a deployment whose meta omits `gw-12`, all
  GW-12 tests report skip, and the exit code is 0.
- **GW-10.AC-6** — The container image runs with only the three
  environment variables set and produces `conformance-report.json`.

## Non-goals

- Not a load/performance benchmark; the only latency assertions are the
  explicit ones in GW-5/GW-9/GW-11.
- Not a fuzzer or security scanner.
- No testing of provider *quality* (model outputs) — the mock provider
  returns canned completions; correctness of LLM content is out of
  scope.
- No UI testing (there is no required UI, GW-6).
