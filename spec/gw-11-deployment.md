# GW-11 — Deployment story

**Status:** Specified · **Plane:** Operational · **Depends on:** —

## Motivation

CogniGate's adoption unit is "run it next to your app". That must be
boring: one command for dev, one compose file for production sidecar
use, predictable resource cost, and shutdowns that never eat an
in-flight completion. If trying CogniGate takes an afternoon of
infrastructure, downstream projects will hardcode a provider SDK
instead.

## Behavioral requirements

### Deployment modes

1. **docker-compose sidecar (reference mode).** The repository's
   `docker-compose.yml` MUST bring up gateway (:8080), analytics
   (:8081), Postgres 16, and Redis 7 with a single
   `docker compose up -d`, healthy within 60 seconds on a warm image
   cache. This is the deployment the conformance suite (GW-10) and all
   documentation treat as canonical. Downstream products embed these
   services into their own compose file / pod spec.
2. **Zero-config dev mode.** `cognigate --dev` (gateway binary or
   `docker run … --dev`) MUST start a single process with **no external
   dependencies** — in-memory stores standing in for Redis/Postgres —
   pre-seeded with one tenant, one printed `cg-dev-…` key, one printed
   root `cga-dev-…` key, and the well-known aliases (GW-2). Data is
   ephemeral; the mode banner MUST state that. Provider keys still come
   from env (`OPENAI_API_KEY` etc. auto-registered when present). The
   full data plane and admin plane MUST work; durability and multi-node
   features need not.
3. **Single-binary gateway.** The Go gateway MUST build as one static
   binary (`CGO_ENABLED=0`) so bare-metal/VM deployments are possible;
   analytics remains a JVM service and is REQUIRED for the admin plane,
   metering durability, and webhooks — a gateway pointed at no analytics
   MUST keep serving the data plane (metering buffered, then dropped
   with a logged warning after the buffer fills) rather than failing
   requests. This is the honest-degradation rule applied to CogniGate
   itself.

### TLS

- Default listeners are plaintext HTTP, on the assumption of a private
  network or an operator-owned reverse proxy — the documented and
  RECOMMENDED production shape.
- Native TLS MUST be available on both listeners via `TLS_CERT_FILE` /
  `TLS_KEY_FILE` for deployments without a proxy. mTLS is out of scope
  (see non-goals).

### Resource footprint

Normative expectations on the reference compose deployment (idle = up,
no traffic; loaded = 50 req/s of mocked completions):

| Component | Idle RSS | p50 added latency |
| --------- | -------- | ----------------- |
| Gateway | ≤ 64 MiB | ≤ 5 ms per proxied request (loopback mock) |
| Analytics | ≤ 512 MiB | — (off the request path) |

The gateway MUST stay off the hot path for anything analytics does:
telemetry dispatch is asynchronous and non-blocking (existing Phase-1
behavior, now contractual).

### Graceful shutdown / drain

- On SIGTERM, the gateway MUST: stop accepting new connections, keep
  serving in-flight requests **including open SSE streams** up to
  `shutdown.drain_timeout` (default 30 s), flush buffered telemetry,
  then exit 0. Requests arriving during drain get 503 with
  `Connection: close`.
- `GET /healthz` MUST start failing at the beginning of drain so load
  balancers stop routing before connections are refused.
- Analytics on SIGTERM MUST finish persisting received telemetry before
  exit. A gateway restart MUST NOT lose accepted-and-completed requests'
  usage records (at-most `telemetry.buffer` records at risk, default
  1000, documented).

## Configuration surface

| Key / env                     | Default | Meaning |
| ----------------------------- | ------- | ------- |
| `--dev`                       | off     | Zero-config single-process dev mode |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | unset | Enable native TLS when both set |
| `shutdown.drain_timeout`      | `30s`   | In-flight grace period |
| `telemetry.buffer`            | `1000`  | Records buffered when analytics is unreachable |
| `API_PORT` / `ANALYTICS_PORT` | `8080` / `8081` | Listeners |

## Acceptance criteria

- **GW-11.AC-1** — `docker compose up -d` from a clean checkout reaches
  all-healthy within 60 s (warm images), and GW-10's suite passes
  against it.
- **GW-11.AC-2** — `cognigate --dev` starts with no Redis/Postgres
  present, prints a `cg-dev-` and `cga-dev-` key, and serves
  `POST /v1/chat/completions` (mock provider) plus the full `/admin/v1`
  CRUD from GW-6.
- **GW-11.AC-3** — With analytics stopped, data-plane requests continue
  to succeed and a warning is logged; restarting analytics within the
  buffer window results in the buffered usage appearing in
  `GET /v1/usage`.
- **GW-11.AC-4** — With `TLS_CERT_FILE`/`TLS_KEY_FILE` set, the data
  plane serves HTTPS and rejects plaintext.
- **GW-11.AC-5** — SIGTERM during an in-flight streaming completion:
  the stream completes normally, a request sent after SIGTERM gets 503,
  `GET /healthz` fails during drain, and the process exits 0 within
  `drain_timeout` + 5 s.
- **GW-11.AC-6** — Under the reference load profile, gateway RSS and
  added p50 latency stay within the table above (measured by the
  suite's optional `-perf` mode; informative until CI hardware is
  pinned, then normative).

## Non-goals

- No Kubernetes manifests/Helm chart maintained as a contract (examples
  MAY live in `docs/`, untested).
- No high-availability story (multi-gateway behind one Redis works but
  is not yet specified or tested); single-node is the supported shape.
- No mTLS, no built-in ACME/Let's Encrypt — bring a proxy.
- No auto-update mechanism.
- Dev mode makes no durability promises whatsoever.
