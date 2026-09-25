FROM golang:1.27-alpine AS development

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN go install github.com/swaggo/swag/cmd/swag@v1.16.3

EXPOSE 9000 9443

# Dev (source bind-mounted at /app): never rewrite go.mod/go.sum on start —
# run `go mod tidy` yourself when changing dependencies.
CMD ["sh", "-c", "go mod download && (swag init -g cmd/server/main.go -o docs/swagger --quiet || echo 'WARNING: swag init failed; using committed docs') && go run ./cmd/server"]

# Production builder stage (API only — the published image is built from the
# repository-root Dockerfile, which also embeds the UI).
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Reproducible: download exactly what go.mod/go.sum pin (no `go mod tidy`).
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Regenerate Swagger docs with the pinned swag version; the generated docs are
# committed, so a failure falls back to them but is reported, not hidden.
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.3 && \
    (swag init -g cmd/server/main.go -o docs/swagger --quiet || \
     echo "WARNING: swag init failed — building with the committed docs/swagger")

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o main ./cmd/server

FROM alpine:latest AS production

RUN apk --no-cache upgrade && apk --no-cache add ca-certificates wget && \
    addgroup -S -g 10001 bkt && adduser -S -D -H -u 10001 -G bkt -h /app -s /sbin/nologin bkt && \
    mkdir -p /data/buckets && chown -R 10001:10001 /data

WORKDIR /app

COPY --from=builder /app/main .

# 9443 = console, 9000 = S3 API (both unprivileged ports)
EXPOSE 9000 9443

USER 10001:10001

CMD ["./main"]
