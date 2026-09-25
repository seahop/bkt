# =============================================================================
# bkt — combined image build (root context)
#
# Builds the React UI, embeds it into the Go binary, and produces two targets:
#   - backend : UI-inclusive API server (no DB)         -> multi-container path
#   - omnibus : backend + bundled Postgres in one image -> single-container path
#
# Build context MUST be the repository root so both frontend/ and backend/ are
# reachable:
#   docker build --target backend  -t bkt-backend .
#   docker build --target omnibus  -t bkt          .
# =============================================================================

# ── Stage 1: build the frontend ──────────────────────────────────────────────
FROM node:26-alpine AS web
WORKDIR /web
COPY frontend/package*.json ./
# package-lock.json is committed: npm ci installs exactly the locked tree.
RUN npm ci
COPY frontend/ ./
# `npm run build` == `tsc && vite build`: run the strict TypeScript type-check
# gate before bundling so type errors fail the image build instead of shipping.
RUN npm run build          # -> /web/dist

# ── Stage 2: build the Go binary with the UI embedded ────────────────────────
FROM golang:1.27-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
# Module download first (cached layer). `go mod download` fetches exactly what
# go.mod/go.sum pin — unlike `go mod tidy`, it never rewrites them, so the
# build is reproducible from the committed files.
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Overlay the real Vite build over the committed placeholder before go:embed.
RUN rm -rf internal/web/dist
COPY --from=web /web/dist ./internal/web/dist
# Regenerate the Swagger spec with the swag version pinned in go.mod. The
# generated docs are also committed, so a generator failure is not fatal — but
# it is reported instead of being silently swallowed.
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.3 && \
    (swag init -g cmd/server/main.go -o docs/swagger --quiet || \
     echo "WARNING: swag init failed — building with the committed docs/swagger")
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o main ./cmd/server

# ── Stage 3a: UI-inclusive backend (multi-container path) ─────────────────────
FROM alpine:latest AS backend
RUN apk --no-cache upgrade && apk --no-cache add ca-certificates wget
# Unprivileged runtime user (fixed numeric IDs so Kubernetes runAsNonRoot can
# verify it and volumes can be pre-chowned: 10001:10001).
RUN addgroup -S -g 10001 bkt && adduser -S -D -H -u 10001 -G bkt -h /app -s /sbin/nologin bkt && \
    mkdir -p /data/buckets && chown -R 10001:10001 /data
WORKDIR /app
COPY --from=builder /app/main .
# 9443 = console (UI + REST), 9000 = S3 API — both > 1024, no privileges needed.
EXPOSE 9000 9443
# Writes go only to STORAGE_ROOT (/data/buckets) and the temp dir (/tmp, for
# async/multipart upload staging). Volumes created by older root-run releases
# must be chowned to 10001:10001 once (docker-compose.prod.yml does this with
# its init-perms service; the Helm chart uses fsGroup).
USER 10001:10001
ENTRYPOINT ["./main"]

# ── Stage 3b: omnibus — backend + bundled Postgres (single-container path) ─────
FROM postgres:18-alpine AS omnibus
# postgresql16 provides the previous major's binaries (/usr/libexec/postgresql16)
# so the entrypoint can pg_upgrade an existing /data/pgdata cluster in place.
RUN apk --no-cache upgrade && apk add --no-cache bash openssl tini su-exec ca-certificates wget postgresql16
# The backend runs as this unprivileged user (the entrypoint starts as root only
# to prepare /data and run Postgres as `postgres`, then drops to bkt).
RUN addgroup -S -g 10001 bkt && adduser -S -D -H -u 10001 -G bkt -h /data -s /sbin/nologin bkt
COPY --from=builder /app/main /usr/local/bin/bkt
COPY docker/omnibus/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# All state (Postgres data, local buckets, certs, secrets) lives under /data.
ENV PGDATA=/data/pgdata
VOLUME /data
# 9443 = console (UI + REST), 9000 = S3 API
EXPOSE 9000 9443
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/entrypoint.sh"]
