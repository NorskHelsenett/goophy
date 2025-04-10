FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go mod file
COPY go.mod ./
# Copy source code
COPY main.go ./

# Download dependencies and build a static binary
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s -extldflags '-static'" -o goophy .

# Create a minimal production image from scratch (no OS)
FROM scratch

WORKDIR /app

# Copy the static binary from the builder stage
COPY --from=builder /app/goophy .

# Set environment variables with defaults
ENV PORT=8080
ENV OLLAMA_ENDPOINT=http://localhost:11434
ENV API_KEY=""

# Expose the port
EXPOSE 8080

# Run the application
CMD ["/app/goophy"]