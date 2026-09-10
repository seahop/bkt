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
FROM node:24-alpine AS web
WORKDIR /web
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
# `npm run build` == `tsc && vite build`: run the strict TypeScript type-check
# gate before bundling so type errors fail the image build instead of shipping.
RUN npm run build          # -> /web/dist

# ── Stage 2: build the Go binary with the UI embedded ────────────────────────
FROM golang:1.27-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY backend/ ./
# Overlay the real Vite build over the committed placeholder before go:embed.
RUN rm -rf internal/web/dist
COPY --from=web /web/dist ./internal/web/dist
RUN go mod tidy
RUN go install github.com/swaggo/swag/cmd/swag@latest && \
    swag init -g cmd/server/main.go -o docs/swagger --quiet 2>/dev/null || true
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/server

# ── Stage 3a: UI-inclusive backend (multi-container path) ─────────────────────
FROM alpine:latest AS backend
RUN apk --no-cache add ca-certificates wget
WORKDIR /app
COPY --from=builder /app/main .
# 9443 = console (UI + REST), 9000 = S3 API
EXPOSE 9000 9443
ENTRYPOINT ["./main"]

# ── Stage 3b: omnibus — backend + bundled Postgres (single-container path) ─────
FROM postgres:16-alpine AS omnibus
RUN apk add --no-cache bash openssl tini su-exec ca-certificates wget
COPY --from=builder /app/main /usr/local/bin/bkt
COPY docker/omnibus/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# All state (Postgres data, local buckets, certs, secrets) lives under /data.
ENV PGDATA=/data/pgdata
VOLUME /data
# 9443 = console (UI + REST), 9000 = S3 API
EXPOSE 9000 9443
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/entrypoint.sh"]
