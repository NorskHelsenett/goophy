# Ollama Proxy

A simple proxy server for Ollama that adds authentication to requests.

## Overview

This proxy server forwards all requests to an Ollama API endpoint while adding an `Authorization: Bearer` token header for authentication. It supports all Ollama API endpoints and preserves original request methods, paths, headers, and bodies.

## Configuration

The proxy uses the following environment variables:

- `PORT`: The port to run the proxy server on (default: `8080`)
- `OLLAMA_ENDPOINT`: The target Ollama endpoint to forward requests to (default: `http://localhost:11434`)
- `API_KEY`: The API key to use for authentication (default: empty)

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