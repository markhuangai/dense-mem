# syntax=docker/dockerfile:1

FROM golang:1.26.6-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/oauth-compat-harness ./cmd/oauth-compat-harness

FROM alpine:3.24

RUN apk add --no-cache ca-certificates && \
    addgroup -S densemem && \
    adduser -S -G densemem -H -s /sbin/nologin densemem
COPY --from=builder --chown=densemem:densemem /out/oauth-compat-harness /app/oauth-compat-harness

USER densemem
EXPOSE 9445
ENTRYPOINT ["/app/oauth-compat-harness"]
