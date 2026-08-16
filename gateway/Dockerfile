# CogniGate edge gateway.
#
# The result is a static binary on a minimal base: CGO_ENABLED=0 is a GW-11
# requirement, not an optimisation, because a gateway that needs a libc at the
# version it was linked against is not a single artefact an operator can copy.

FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION is stamped into the binary so /healthz and /v1/meta report something
# an operator can match to a release. The default is semver rather than the word
# "dev" because GW-9 publishes it as a version a client may parse; the
# prerelease segment is what marks an untagged build.
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X main.version=${VERSION}" -o cognigate .

FROM alpine:3.24

# ca-certificates so the gateway can dial provider APIs over TLS; wget is
# busybox's and is what the container healthcheck below runs.
RUN apk add --no-cache ca-certificates

# An unprivileged user: nothing the gateway does needs root, and the process
# writes nothing to disk.
RUN adduser -D -u 10001 cognigate
USER cognigate

WORKDIR /app
COPY --from=builder /app/cognigate .

EXPOSE 8080

# /healthz is dependency-free and starts failing the moment a drain begins, so
# it is exactly the signal an orchestrator should steer on.
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=6 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["./cognigate"]
