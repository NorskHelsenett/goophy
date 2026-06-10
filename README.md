# Ollama Proxy

A simple proxy server for Ollama that adds authentication to requests.

![goopy](.github/.docs/goophy.jpg)

## Overview

This proxy server forwards all requests to an Ollama API endpoint while adding an `Authorization: Bearer` token header for authentication. It supports all Ollama API endpoints and preserves original request methods, paths, headers, and bodies.

## Configuration

The proxy can be configured with environment variables or, equivalently, with
command-line flags. Each option supports **both syntaxes** — when a flag and its
environment variable are both set, the flag wins.

| Env variable            | Flag (`serve`)    | Default                  | Description                                       |
| ----------------------- | ----------------- | ------------------------ | ------------------------------------------------- |
| `HOST`                  | `--host`          | `127.0.0.1`              | Interface to bind to (`--listen-all` = `0.0.0.0`) |
| `PORT`                  | `--port`          | `22434`                  | Port to run the proxy server on                   |
| `API_ENDPOINT`          | `--api-endpoint`  | `http://localhost:11434` | Target API endpoint to forward requests to        |
| `API_KEY`               | `--api-key`       | _(empty)_                | API key used for authentication                   |
| `DISABLE_AUTO_UPDATE`   | —                 | `false`                  | Disable auto-update                               |
| `UPDATE_CHECK_INTERVAL` | —                 | `24h`                    | How often to check for updates                    |

> **Deprecated:** `OLLAMA_ENDPOINT` / `--ollama-endpoint` still work as aliases
> for `API_ENDPOINT` / `--api-endpoint`, but print a warning. Prefer the new
> names; the aliases may be removed in a future release.

Configuration can be provided in multiple ways:

1. Command-line flags: `goophy serve --port 22434 --api-endpoint http://localhost:11434 --api-key sk-...`
   (the `--flag=value` form works too, e.g. `--port=22434`)
2. Environment variables: `PORT=22434 API_ENDPOINT=http://localhost:11434 goophy serve`
3. From a `.env` file in the current directory (automatically loaded)
4. From a custom env file specified with the `--env-file` flag: `goophy --env-file my-config.env serve`

Flags and `--env-file` may be placed **before or after** the command — `goophy serve --env-file my-config.env`
and `goophy --env-file my-config.env serve` are equivalent.

See `example.env` for a sample configuration file format.

## Usage

### Get Started

#### 1. Download the binaries from releases

[https://github.com/NorskHelsenett/goophy/releases](https://github.com/NorskHelsenett/goophy/releases)

#### 2. Docker

Create a `.env` file with the following content:

```.env
API_ENDPOINT="https://openwebui.com/ollama"
API_KEY="sk-your-key-from-OWUI"
```
Then run the following container:

```bash
 docker run --rm --env-file .env -p 22434:22434 ghcr.io/norskhelsenett/goophy serve
````

### Building and Running

```bash
# Build the application
go build -o goophy cmd/goophy/main.go

# Or with custom version
go build -ldflags="-X main.version=0.1.3" -o goophy ./cmd/goophy/

# Run the proxy with default settings
./goophy serve

# Run with custom settings using flags
./goophy serve --port 22434 --api-endpoint https://my-api-server.example.com/v1 --api-key my-secret-key

# ...or the equivalent environment variables
PORT=22434 API_ENDPOINT=https://my-api-server.example.com/v1 API_KEY=my-secret-key ./goophy serve

# Run with settings from a config file (flag works before or after the command)
./goophy --env-file my-config.env serve
./goophy serve --env-file my-config.env

# A .env file in the current directory is automatically loaded
# echo "API_KEY=my-secret-key" > .env
# ./goophy serve
```

### Docker

You can also build and run this proxy in Docker:

```bash
# Build the Docker image
docker build -t ollama-proxy .

# Run the container
docker run -p 22434:22434 -e API_ENDPOINT=https://my-api-server.example.com/v1 -e API_KEY=my-secret-key ollama-proxy
```

### OpenWebUI

OpenWebUI is accessible at the `/ollama` endpoint, so using it would be like `example.com/ollama`.

### Multi-platform Builds

This application is built for multiple platforms:
- macOS (Intel/AMD64 and Apple Silicon/ARM64)
- Linux (AMD64 and ARM64)
- Windows (AMD64 and ARM64)

You can download the latest release from the [releases page](https://github.com/goophy/goophy/releases).

> To run this on MacOSX due to Gatekeeper, you have to add this binary to the allowlist by running `xattr -d com.apple.quarantine ~/.local/bin/goophy`

### Tips

```bash
echo 'export OLLAMA_HOST="http://localhost:22434"' >> .zshrc
```

By exporting the `OLLAMA_HOST` environment its possible to use `ollama ps|run|pull|rm...` without specifying this environment for every request.

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
- `/api/models` - List available models (OpenAI-compatible)

## Example

```bash
# Call the proxy to chat with a model
curl -X POST http://localhost:22434/api/chat -d '{
  "model": "llama3.2",
  "messages": [{"role": "user", "content": "Hello, how are you?"}]
}'
```

The proxy will forward this request to your Ollama endpoint with the added authentication header and return the response.
