# Running the conformance suite

This suite certifies a *running deployment*, not this source tree. It talks to a
gateway over HTTP, provisions everything it needs under names derived from its
own run id, and removes all of it afterwards. Pointing it at someone else's
CogniGate is the intended use.

Every acceptance criterion in `spec/` is either exercised or explicitly reported
as not run. There is no third state, and no criterion is silently absent — that
property is itself checked (GW-10).

## Quick start

Against the reference deployment in this repository:

```bash
docker compose -f docker-compose.yml -f docker-compose.conformance.yml up -d --wait
```

The overlay adds the two mock upstreams; the base stack has no provider, so the
suite cannot certify completions without it.

Then, from the repository root:

```bash
export CONF_BASE_URL=http://localhost:8080
export CONF_MOCK_PROVIDER=http://mock-provider-a:9900
export CONF_MOCK_CONTROL_URL=http://localhost:19900
export CONF_ADMIN_KEY=$(grep '^GATEWAY_BOOTSTRAP_KEY=' .env | cut -d= -f2- | tr -d '\r')
go test -count=1 -timeout 25m ./conformance/...
```

`tr -d '\r'` matters on Windows checkouts: a trailing carriage return travels
into the Authorization header and every admin call answers 401.

### Letting the suite read the request log

GW-8's first two criteria are about the shape of the gateway's structured log,
which is not observable through the HTTP surface everything else uses. They skip
unless `CONF_LOG_PATH` names a file the suite can read:

```bash
docker compose -f docker-compose.yml -f docker-compose.conformance.yml \
  logs -f --no-color --no-log-prefix gateway > gateway.log &
export CONF_LOG_PATH=$PWD/gateway.log
```

It must be a file something is still *appending to*. The suite marks a byte
offset before each request and reads forward from it, so a snapshot taken with
`docker compose logs` (no `-f`) makes both tests fail rather than skip.
`--no-log-prefix` is not optional either — compose's usual `gateway  | ` prefix
makes every JSON line unparseable.

## Environment

| Variable | Required | What it does |
| --- | --- | --- |
| `CONF_BASE_URL` | yes | The gateway's base URL. Without it every test skips and the report still lists the full inventory, so `go test ./...` at the repository root stays honest on a machine with no gateway on it. |
| `CONF_ADMIN_KEY` | yes | A key the admin plane accepts. For the reference stack this is `GATEWAY_BOOTSTRAP_KEY`, which is deliberately *not* a `cga-` key — the suite checks only that the admin plane accepts it, never its shape. |
| `CONF_MOCK_PROVIDER` | no | The mock's base URL **as the gateway dials it**. Defaults to `embedded`, which hosts the mock inside the test process and therefore only works when the suite and the gateway share a host. For a containerised gateway this is a service name on its network. |
| `CONF_MOCK_CONTROL_URL` | no | The mock's base URL **as the suite dials it**. Defaults to `CONF_MOCK_PROVIDER`, and differs only when the two sides reach the mock by different names — which is exactly the compose case above. |
| `CONF_METRICS_TOKEN` | no | Credential for `/metrics`. GW-8 leaves that endpoint unauthenticated by default, so this is empty unless the deployment chose otherwise. |
| `CONF_LOG_PATH` | no | See above. Empty means GW-8.AC-1 and GW-8.AC-2 report "not run" rather than failing — a gateway that logs to stdout in a container is conformant and simply unreadable from here. |
| `CONF_REPORT` | no | Where the JSON report is written. Defaults to `conformance-report.json` in the working directory. |
| `CONF_PERF` | no | Opt-in for GW-11.AC-6, which measures added latency and idle RSS. Off by default because it is a measurement of the host as much as of the gateway. |

## Two runs certify a deployment, not one

`quotas.enforcement` changes what the gateway *does*, so no single run can cover
both settings. Criteria that need hard caps to reject skip under `observe`, and
GW-4.AC-8 — which requires an over-cap request to be reported and admitted —
skips under `on`. The suite reads the mode from `/v1/meta` rather than asking you
to keep an environment variable in step with the deployment.

The reference stack threads the setting through, so the second run is a restart:

```bash
QUOTA_ENFORCEMENT=observe docker compose \
  -f docker-compose.yml -f docker-compose.conformance.yml \
  up -d --wait gateway

CONF_REPORT=$PWD/conformance-report-observe.json \
  go test -count=1 -timeout 25m ./conformance/...
```

Restart the log follower afterwards if you are using one — the old one is still
attached to the container that was just replaced.

CI does both passes and asserts GW-4.AC-8 passed *by name* in the second report.
Checking the exit code alone would not do: every enforcement-dependent criterion
skips in observe mode, so a second pass that had not actually switched modes
would still be green while certifying nothing new.

## Reading the result

The report is the artefact, not the terminal output:

```bash
python -c "
import json,collections
d=json.load(open('conformance-report.json'))
print(collections.Counter(r['status'] for r in d['results']))
print([r['id'] for r in d['results'] if r['status'] != 'pass'])
"
```

Counts come from the report rather than from `go test` output because a criterion
can be covered by more than one test, and because skips carry the reason.

## Things that will bite you

**`-count=1` is not optional.** Go caches results by package and flags. A rerun
whose only change is the *deployment's* configuration is, as far as the cache is
concerned, the same test — so without it the second quota run happily reprints
the first run's verdict.

**A `-run`-filtered run always ends `FAIL` at the package level.** `TestMain`
cross-checks what the target claims at `/v1/meta` against what actually ran, and
a filter means most of it did not. The per-test `--- PASS` / `--- FAIL` lines are
the real result; the package line is the completeness check reacting to the
filter. Use a filter to iterate, never to certify.

**GW-11.AC-5 skips on Windows.** It signals a running gateway with `SIGTERM` and
asserts an in-flight stream still completes; there is no POSIX signal to send.
To exercise it, run on Linux:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26 \
  go test -count=1 -timeout 15m -run TestGW11_AC5 ./conformance/
```

Prefix that with `MSYS_NO_PATHCONV=1` under Git Bash, which otherwise rewrites
`-w /src` into a Windows path and the container refuses to start. This is the
one filtered run worth making: the criterion spawns its own gateway and needs no
target, so the package-level `FAIL` above does not describe it.

**GW-11.AC-6 reads idle RSS only where `/proc` offers it.** Elsewhere it logs
that and checks the latency half of the table alone.

**"The gateway never listed the mock's models."** `CONF_MOCK_PROVIDER` is the
address *the gateway* dials, not the one you can reach. A containerised gateway
cannot resolve `localhost:19900`; it needs the service name. Set
`CONF_MOCK_CONTROL_URL` for your side of it.
