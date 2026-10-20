# ── Stage 1: Build the Go binary ──────────────────────────────────────────────
FROM golang:1.27.2-alpine3.24 AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates tzdata

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/server cmd/server/main.go

# ── Stage 2: Minimal runtime image ──────────────────────────────────────────
FROM alpine:3.24

WORKDIR /app

# Install ca-certificates and curl for healthcheck
RUN apk add --no-cache ca-certificates tzdata curl yt-dlp

# Copy binary from builder
COPY --from=builder /app/server /app/server

# Expose port
EXPOSE 8008

# Run as non-root user
USER nobody:nobody

ENTRYPOINT ["/app/server"]
