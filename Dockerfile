# syntax=docker/dockerfile:1.7

# ============================================================================
# Web portal build stage
# ============================================================================
FROM node:22-alpine AS web-builder

WORKDIR /web

COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# ============================================================================
# Build stage
# ============================================================================
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Module download in its own layer so source edits do not bust the cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO=0 produces a static binary that runs on alpine without libc shims.
# -trimpath strips build-host paths; -ldflags="-s -w" drops symbol + DWARF
# tables (smaller image, no debugger support in prod).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/provision-profile ./cmd/provision-profile && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/list-profiles ./cmd/list-profiles && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/delete-profile ./cmd/delete-profile && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/list-keys ./cmd/list-keys && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/delete-key ./cmd/delete-key && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/rotate-key ./cmd/rotate-key && \
    cp /out/provision-profile /out/provision-team && \
    cp /out/list-profiles /out/list-teams && \
    cp /out/delete-profile /out/delete-team && \
    cp /out/list-keys /out/list-team-profiles && \
    cp /out/delete-key /out/delete-team-profile && \
    cp /out/rotate-key /out/rotate-team-profile-key

# ============================================================================
# Runtime stage
# ============================================================================
FROM alpine:3.20

ARG IMAGE_VERSION=dev
ARG IMAGE_REVISION=unknown
ARG IMAGE_CREATED=unknown

LABEL org.opencontainers.image.title="Dense-Mem" \
      org.opencontainers.image.description="Standalone HTTP MCP memory server with profile-scoped recall, claims, and local control portal." \
      org.opencontainers.image.url="https://github.com/markhuangai/dense-mem" \
      org.opencontainers.image.source="https://github.com/markhuangai/dense-mem" \
      org.opencontainers.image.documentation="https://github.com/markhuangai/dense-mem#readme" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.created="${IMAGE_CREATED}"

# ca-certificates for outbound TLS (Postgres/Neo4j/Redis if TLS-enabled);
# tzdata for correct UTC handling in audit timestamps; wget for HEALTHCHECK.
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S densemem && \
    adduser -S -G densemem -H -s /sbin/nologin densemem

WORKDIR /app

COPY --from=builder /out/server  /app/server
COPY --from=builder /out/migrate /app/migrate
COPY --from=builder /out/provision-profile /app/provision-profile
COPY --from=builder /out/provision-team /app/provision-team
COPY --from=builder /out/list-profiles /app/list-profiles
COPY --from=builder /out/list-teams /app/list-teams
COPY --from=builder /out/delete-profile /app/delete-profile
COPY --from=builder /out/delete-team /app/delete-team
COPY --from=builder /out/list-keys /app/list-keys
COPY --from=builder /out/list-team-profiles /app/list-team-profiles
COPY --from=builder /out/delete-key /app/delete-key
COPY --from=builder /out/delete-team-profile /app/delete-team-profile
COPY --from=builder /out/rotate-key /app/rotate-key
COPY --from=builder /out/rotate-team-profile-key /app/rotate-team-profile-key

# migrator.go discovers migrations via cwd-relative walk; WORKDIR=/app plus
# this copy satisfies Strategy 1 in getMigrationsDir().
COPY --chown=densemem:densemem migrations /app/migrations

COPY --from=web-builder --chown=densemem:densemem /web/dist /app/web/dist

# Entrypoint wrapper assembles POSTGRES_DSN from component env vars if the
# DSN is not supplied directly. Keeps the full credentialed URL literal out
# of every tracked config file.
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

USER densemem

EXPOSE 8080 8090

# /health is a liveness probe (process up); /ready flips to 503 on transient
# dependency blips which would force Docker to restart a healthy container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget --quiet -O /dev/null http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/server"]
