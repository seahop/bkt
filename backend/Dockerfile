FROM golang:1.25-alpine AS development

# Install required packages
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod and source files
COPY go.mod ./
COPY . .

# Download dependencies and update go.sum
RUN go mod tidy

# Expose port
EXPOSE 9000

# Run go mod tidy then start the server
CMD ["sh", "-c", "go mod tidy && go run ./cmd/server"]

# Production stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY . .
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/server

FROM alpine:latest AS production

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/main .

EXPOSE 9000

CMD ["./main"]
