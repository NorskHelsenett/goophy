FROM golang:1.26-alpine AS builder

# Install ca-certificates
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy go mod file
COPY go.mod go.sum ./
# Copy source code
COPY . ./

# Download dependencies and build a static binary
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -extldflags '-static'" -o goophy ./cmd/goophy

# Final minimal image
FROM scratch

WORKDIR /app

# Copy the static binary and CA certs from builder
COPY --from=builder /app/goophy .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Set environment variables with defaults
ENV PORT=22434
ENV OLLAMA_ENDPOINT=http://localhost:11434
ENV API_KEY=""
ENV DISABLE_AUTO_UPDATE=true

# Expose the port
EXPOSE 22434

# Run the application - use ENTRYPOINT with CMD to allow passing arguments
ENTRYPOINT ["/app/goophy"]
CMD []
