# CogniGate conformance suite (GW-10).
#
# Two things ship from this file, because they are built from the same module
# and always used together:
#
#   --target mock    the fake upstream the suite drives (conformance/cmd/mockprovider)
#   --target suite   the suite itself, as a compiled test binary (the default)
#
# The suite is compiled with `go test -c` rather than run with `go test`, so the
# image carries no Go toolchain and no module cache. That is what lets it start
# from the three environment variables GW-10 documents and nothing else: there
# is no build to configure, only a binary to run.
#
#   docker run --rm --network <deployment-net> \
#     -e CONF_BASE_URL=http://gateway:8080 \
#     -e CONF_ADMIN_KEY=<admin key> \
#     -e CONF_MOCK_PROVIDER=http://mock-provider-a:9900 \
#     cognigate-conformance:<version>
#
# CONF_MOCK_CONTROL_URL is not in that list on purpose. It exists for a run from
# outside the deployment, where the gateway reaches the mock by a service name
# the suite cannot resolve; a container on the deployment's own network dials
# the same name the gateway does, so the two collapse into one variable.

FROM golang:1.26-alpine AS builder

WORKDIR /src

# The suite is standard library only and has no go.sum, so there is nothing to
# download and nothing to verify — copying the module file first still keeps the
# layer cache useful when only the tests change.
COPY go.mod ./
COPY conformance ./conformance

# CGO_ENABLED=0 for the same reason the gateway sets it: a single artefact that
# runs on the base image it was copied onto rather than the one it was built on.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/mockprovider ./conformance/cmd/mockprovider
RUN CGO_ENABLED=0 GOOS=linux go test -c -o /out/conformance.test ./conformance/

# --- the mock upstream -------------------------------------------------------

FROM alpine:3.24 AS mock

RUN adduser -D -u 10001 conformance
USER conformance

COPY --from=builder /out/mockprovider /usr/local/bin/mockprovider

EXPOSE 9900

ENTRYPOINT ["mockprovider"]

# --- the suite ---------------------------------------------------------------

FROM alpine:3.24 AS suite

# ca-certificates so the suite can reach a deployment served over TLS.
RUN apk add --no-cache ca-certificates

RUN adduser -D -u 10001 conformance

# /work is where conformance-report.json lands. It belongs to the suite's user
# so the report can be written when the image runs unprivileged, and it is the
# directory an operator mounts over to collect the report.
RUN mkdir -p /work && chown conformance:conformance /work

# The GW-10 criteria read this repository. The coverage check parses spec/ for
# the criteria and conformance/*_test.go for the tests that cover them, and the
# wiring checks parse .github/workflows/ and this Dockerfile. So the image
# carries the sources beside the binary — without them the containerised run
# would fail exactly the criteria that are about the container, and for a reason
# that has nothing to do with the deployment under test.
COPY --from=builder /src/go.mod /repo/go.mod
COPY conformance /repo/conformance
COPY spec /repo/spec
COPY .github/workflows /repo/.github/workflows
COPY CHANGELOG.md /repo/CHANGELOG.md
COPY docker-compose.yml docker-compose.conformance.yml /repo/
COPY --from=builder /out/conformance.test /repo/conformance/conformance.test

RUN chown -R conformance:conformance /repo
USER conformance

# The suite's own directory, because `go test` runs a package there and the
# criteria that walk up to the repository root expect to start from it.
WORKDIR /repo/conformance

# Written where an operator can mount over it, and named exactly as the
# specification names it (GW-10.AC-6).
ENV CONF_REPORT=/work/conformance-report.json

# -test.v so the run is readable in a CI log, and -test.count=1 because a test
# binary caches nothing but the flag documents that a rerun is a real rerun.
ENTRYPOINT ["./conformance.test", "-test.v", "-test.count=1"]
