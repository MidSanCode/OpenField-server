# syntax=docker/dockerfile:1
#
# One image per OpenField server service. The repository is a Go workspace
# (go.work) whose modules all `replace github.com/openfield/server/pkg => ../../pkg`,
# so the whole tree has to be present for any single service to compile; the
# SERVICE build arg then picks which `services/<name>/cmd` to build.
#
#   docker build --build-arg SERVICE=gateway -t openfield-gateway .
#
# The resulting image runs one service in the foreground and reads its
# configuration from $OPENFIELD_CONFIG (default /etc/openfield/config.yaml).

ARG GO_VERSION=1.25

# ---------- build ----------
FROM golang:${GO_VERSION}-bookworm AS build

ARG SERVICE=gateway

WORKDIR /src

# go.work ties the modules together, and go.work.sum pins the union of their
# dependencies, so both must be present before `go build` runs.
COPY go.work go.work.sum ./
COPY go.mod go.sum ./
COPY pkg/ ./pkg/
COPY services/ ./services/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    test -d "services/${SERVICE}/cmd" || { \
        echo "unknown SERVICE='${SERVICE}' (expected services/<name>/cmd)" >&2; exit 1; \
    }; \
    cd "services/${SERVICE}"; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/openfield-server ./cmd; \
    ls -l /out/openfield-server

# ---------- runtime ----------
# Alpine keeps ca-certificates (Postgres TLS, S3, OIDC) and tzdata available -
# the config has a timezone knob that needs /usr/share/zoneinfo - and is tiny.
FROM alpine:3.20 AS runtime

ARG SERVICE=gateway
ARG VERSION=dev
ARG PORT=8080

LABEL org.opencontainers.image.title="openfield-${SERVICE}" \
      org.opencontainers.image.description="OpenField server - ${SERVICE} service" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/MidSanCode/OpenField-server"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 openfield \
    && adduser -S -D -H -u 10001 -G openfield openfield \
    && mkdir -p /etc/openfield /app/data/plugins \
    && chown -R openfield:openfield /etc/openfield /app/data

WORKDIR /app

COPY --from=build --chown=root:root /out/openfield-server /app/openfield-server

# Shipped as a starting point only: the real deployment mounts a config file at
# $OPENFIELD_CONFIG (or overrides every value with the documented env vars).
COPY config/config.example.yaml /etc/openfield/config.example.yaml

# The plugin service writes uploaded bundles under OPENFIELD_PLUGIN_DIR (it
# defaults to the relative data/plugins, which would be unwritable here because
# /app belongs to root). /app/data is created writable above; mount it if the
# bundles should survive a container restart.
ENV OPENFIELD_SERVICE=${SERVICE} \
    OPENFIELD_VERSION=${VERSION} \
    OPENFIELD_CONFIG=/etc/openfield/config.yaml \
    OPENFIELD_PLUGIN_DIR=/app/data/plugins \
    PORT=${PORT}

EXPOSE ${PORT}

USER openfield:openfield

# A service exits on its own when it cannot load its config, so a missing
# config fails fast instead of idling.
ENTRYPOINT ["/app/openfield-server"]
