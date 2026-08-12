# syntax=docker/dockerfile:1

# ============================================================================
# Web portal build stage
# ============================================================================
FROM --platform=$BUILDPLATFORM node:24-alpine AS web-builder

WORKDIR /web

COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm,sharing=locked npm ci

COPY web/ ./
RUN npm run build

# ============================================================================
# Build stage
# ============================================================================
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS builder-base

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Module download in its own layer so source edits do not bust the cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked go mod download

COPY . .

FROM builder-base AS production-builder

# CGO=0 produces a static binary that runs on alpine without libc shims.
# -trimpath strips build-host paths; -ldflags="-s -w" drops symbol + DWARF
# tables (smaller image, no debugger support in prod).
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM builder-base AS evaluation-builder

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags=evaluation -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ============================================================================
# Shared runtime stage
# ============================================================================
FROM alpine:3.24 AS runtime-base

ARG IMAGE_VERSION=dev
ARG IMAGE_REVISION=unknown
ARG IMAGE_CREATED=unknown

LABEL org.opencontainers.image.title="Dense-Mem" \
      org.opencontainers.image.description="Standalone HTTP MCP memory server with evidence-gated recall, lifecycle, and local control portal." \
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

# migrator.go discovers migrations via cwd-relative walk; WORKDIR=/app plus
# this copy satisfies Strategy 1 in getMigrationsDir().
COPY --chown=densemem:densemem migrations /app/migrations

COPY --from=web-builder --chown=densemem:densemem /web/dist /app/web/dist
COPY --from=web-builder --chown=densemem:densemem /web/user-dist /app/web/user-dist

# Entrypoint wrapper preserves signal/exit semantics; the application builds
# and escapes POSTGRES_DSN from component environment variables when needed.
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

USER densemem

EXPOSE 8080 8090

# /health is a liveness probe (process up); /ready flips to 503 on transient
# dependency blips which would force Docker to restart a healthy container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30m --retries=3 \
    CMD sh -c 'addr="${HTTP_ADDR:-:8080}"; port="${addr##*:}"; wget --quiet -O /dev/null "http://127.0.0.1:${port}/health"' || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/server"]

# The evaluation image is built explicitly with --target evaluation. Its
# additional MCP tools are absent from the production binary, not runtime-gated.
FROM runtime-base AS evaluation

LABEL org.opencontainers.image.variant="evaluation"
COPY --from=evaluation-builder --chown=densemem:densemem /out/server /app/server

# Keep production last so an unqualified docker build remains production-only.
FROM runtime-base AS production

LABEL org.opencontainers.image.variant="production"
COPY --from=production-builder --chown=densemem:densemem /out/server /app/server
