# Build on the native build platform and cross-compile to the target arch, so
# the Go toolchain never runs under QEMU emulation (which crashes the Go 1.26 GC
# when building linux/amd64 on an arm64 host).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

# Install ca-certificates
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy go mod file
COPY go.mod go.sum ./
# Copy source code
COPY . ./

# Download dependencies and build a static binary
RUN go mod tidy
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s -extldflags '-static'" -o goophy ./cmd/goophy

# Final minimal image
FROM scratch

WORKDIR /app

# Copy the static binary and CA certs from builder
COPY --from=builder /app/goophy .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Set environment variables with defaults.
# Bind all interfaces inside the container: it is network-isolated, and host
# exposure is controlled by `docker run -p`. (The binary defaults to 127.0.0.1.)
ENV HOST=0.0.0.0
ENV PORT=22434
ENV OLLAMA_ENDPOINT=http://localhost:11434
ENV API_KEY=""
ENV DISABLE_AUTO_UPDATE=true

# Expose the port
EXPOSE 22434

# Run the application - use ENTRYPOINT with CMD to allow passing arguments
ENTRYPOINT ["/app/goophy"]
CMD []
