FROM golang:1.25-alpine AS development

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod ./
COPY . .

RUN go mod tidy && go install github.com/swaggo/swag/cmd/swag@latest

EXPOSE 9000

CMD ["sh", "-c", "go mod tidy && swag init -g cmd/server/main.go -o docs/swagger --quiet 2>/dev/null || true && go run ./cmd/server"]

# Production builder stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod ./
COPY . .

RUN go mod tidy

# Generate Swagger docs before compiling
RUN go install github.com/swaggo/swag/cmd/swag@latest && \
    swag init -g cmd/server/main.go -o docs/swagger --quiet 2>/dev/null || true

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/server

FROM alpine:latest AS production

RUN apk --no-cache add ca-certificates wget

WORKDIR /root/

COPY --from=builder /app/main .

EXPOSE 9000

CMD ["./main"]
