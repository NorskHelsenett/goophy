# Ollama Proxy

A simple proxy server for Ollama that adds authentication to requests.

## Overview

This proxy server forwards all requests to an Ollama API endpoint while adding an `Authorization: Bearer` token header for authentication. It supports all Ollama API endpoints and preserves original request methods, paths, headers, and bodies.

## Configuration

The proxy uses the following environment variables:

- `PORT`: The port to run the proxy server on (default: `8080`)
- `OLLAMA_ENDPOINT`: The target Ollama endpoint to forward requests to (default: `http://localhost:11434`)
- `API_KEY`: The API key to use for authentication (default: empty)
- `GITHUB_TOKEN`: (Optional) GitHub token for auto-updates

## Usage

### Building and Running

```bash
# Build the application
go build -o goophy

# Run the proxy with default settings
./goophy

# Run with custom settings
PORT=9000 OLLAMA_ENDPOINT=https://my-ollama-server.example.com API_KEY=my-secret-key ./goophy
```

### Docker

You can also build and run this proxy in Docker:

```bash
# Build the Docker image
docker build -t ollama-proxy .

# Run the container
docker run -p 8080:8080 -e OLLAMA_ENDPOINT=https://my-ollama-server.example.com -e API_KEY=my-secret-key ollama-proxy
```

### Multi-platform Builds

This application is built for multiple platforms:
- macOS (Intel/AMD64 and Apple Silicon/ARM64)
- Linux (AMD64 and ARM64)
- Windows (AMD64 and ARM64)

You can download the latest release from the [releases page](https://github.com/goophy/goophy/releases).

### Auto-Update System

The application includes an automatic update feature that:

1. Checks for new versions on GitHub when the application starts
2. Performs periodic checks every 24 hours
3. Automatically downloads and applies updates in the background

The auto-update system will handle the different archive formats for each platform:
- `.tar.gz` for macOS and Linux
- `.zip` for Windows

To disable auto-updates, set the environment variable:
```bash
DISABLE_AUTO_UPDATE=true ./goophy
```

You can also customize the update check interval:
```bash
UPDATE_CHECK_INTERVAL=12h ./goophy
```

## API Endpoints

This proxy supports all Ollama API endpoints by forwarding requests to the target Ollama server. Check the [Ollama documentation](https://github.com/ollama/ollama) for a complete list of API endpoints.

Common endpoints include:

- `/api/generate` - Generate text from a prompt
- `/api/chat` - Chat with a model
- `/api/embeddings` - Get embeddings for a text
- `/api/tags` - List available models

## Example

```bash
# Call the proxy to chat with a model
curl -X POST http://localhost:8080/api/chat -d '{
  "model": "llama3.2",
  "messages": [{"role": "user", "content": "Hello, how are you?"}]
}'
```

The proxy will forward this request to your Ollama endpoint with the added authentication header and return the response.