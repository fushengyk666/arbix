# Build Stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy Go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build microservices
# CGO_ENABLED=0 for static binaries
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/collector ./cmd/collector
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/monitor ./cmd/monitor

# Runtime Stage
FROM alpine:3.19

WORKDIR /app
COPY --from=builder /bin/collector /app/collector
COPY --from=builder /bin/monitor /app/monitor

# Install certificates for HTTPS/NATS
RUN apk --no-cache add ca-certificates tzdata

# Default command (overridden by docker-compose)
CMD ["/app/collector"]
