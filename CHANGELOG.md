# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Interactive dashboard for `goophy serve` in a terminal: live session stats
  (requests, errors, tokens, tok/s), token usage over time and per client
  (including prompt-cache hits), a request inspector, per-model usage and a
  filterable log view. Disable with `--no-tui` or `NO_TUI=true`; it is skipped
  automatically when not attached to a terminal, keeping the plain log output.
  In dashboard mode goophy asks OpenAI-compatible upstreams for streamed token
  usage (`stream_options.include_usage`) when the client doesn't set it.

## [0.2.4] - 2026-11-11

### Added
- Requests to the upstream now identify goophy in their `User-Agent`. Proxied
  requests keep the calling client's agent after it
  (e.g. `goophy/0.2.4 pi/1.2.0`), so upstream logs show which agent made each
  call. Goophy's own requests (upstream detection, auto-pull) send just
  `goophy/<version>` instead of Go's default `Go-http-client/1.1`.

## [0.2.3] - 2026-11-04

### Fixed
- `goophy update` now works on macOS and on x86_64 Linux/Windows. The updater
  looked for asset names containing `macos`/`amd64`, but releases are named
  `Darwin`/`x86_64`, so the update check failed with "no compatible binary
  found". Releases now also ship legacy-named archives
  (e.g. `goophy_legacy_macos_arm64.tar.gz`) so older binaries can update to this one.
- Startup only fails on auth when *every* upstream probe is rejected with
  401/403, not when any single one is (e.g. vLLM with `--api-key` answers
  `/health` openly).
- Upstream detection is retried on incoming requests (at most every 30s) while
  the upstream is unreachable, so routing and auto-pull adapt once it comes up.
- Detection requires JSON bodies with the expected keys, so catch-all HTML hosts
  are no longer misclassified.
- A base path ending in `/v1` or `/api` is treated as the OpenAI prefix and
  stripped before probing (no more `/api/api/config`).
- Model names are normalized on bare `/chat/completions` and `/completions`.
- Auto-pull is disabled for Open WebUI, which only exposes
  `/ollama/api/pull`.

### Changed
- Built with Go 1.27; `go-github` upgraded from v50 to v92, which drops several
  indirect dependencies.

## [0.2.2] - 2026-07-04

### Added
- Automatic upstream detection: goophy probes the endpoint at startup and
  identifies whether it's Ollama, vLLM, Open WebUI, or a generic
  OpenAI-compatible server, then adapts its behavior (e.g. auto-pull is only
  attempted against Ollama-style upstreams).
- OpenAI-style path routing: a client can point at goophy with a bare host and
  send `/chat/completions`, `/completions`, `/embeddings`, `/rerank`, or
  `/models`; goophy rewrites the path to the prefix the detected upstream expects
  (`/v1` for vLLM/Ollama/OpenAI, `/api` for Open WebUI).

### Changed
- Bumped dependencies (`github.com/cloudflare/circl`, `golang.org/x/crypto`,
  `golang.org/x/sys`).
- Hardened the auto-updater, fixed the multi-arch Docker build, and added a
  `golangci-lint` configuration.

### Fixed
- An unidentified upstream (e.g. Ollama not yet running when goophy starts) no
  longer disables path routing: bare OpenAI paths still route to `/v1`, so
  `POST /chat/completions` reaches Ollama instead of 404ing.
- OpenAI path routing is now anchored: a shared suffix no longer misroutes
  Ollama-native paths such as `/api/embeddings`, and client `/ollama/...`
  prefixes are preserved.
- Upstream detection honors a base path in `API_ENDPOINT` (e.g.
  `http://gateway/ollama`), probing under it instead of the host root.
- Startup now fails fast when the upstream is reachable but rejects every probe
  with 401/403, surfacing a wrong or missing `API_KEY` immediately instead of
  letting every proxied request fail later.
- A partial request-body read no longer forwards a stale `Content-Length`, which
  the transport rejected as a length/body mismatch.
- Redirect logging is now gated by verbose mode, matching the rest of the
  transport logging.

## [0.2.1] - 2026-06-09

### Added
- `serve` now accepts `--port`, `--api-endpoint`, and `--api-key` flags in
  addition to the existing `--host`. Every option supports both a `--flag` and
  its environment variable, with the flag taking precedence.
- Flags and `--env-file` may be placed before or after the command, in either
  `--flag value` or `--flag=value` form (e.g. both `goophy serve --env-file f`
  and `goophy --env-file f serve` work).

### Changed
- The upstream endpoint is now configured via `API_ENDPOINT` / `--api-endpoint`,
  reflecting that goophy proxies OpenAI-compatible endpoints and not only
  Ollama. `OLLAMA_ENDPOINT` / `--ollama-endpoint` continue to work as deprecated
  aliases (with a warning) and may be removed in a future release.

## [0.2.0] - 2026-06-01

### Added
- Configurable bind interface via `HOST` / `--host` (with `--listen-all` as a
  shorthand). The proxy binds to localhost by default; set `0.0.0.0` to accept
  external connections.
- The running version is now shown in the `serve` startup banner and in
  `--version` output. When the binary is not a tagged release build, the
  version is derived from this changelog so local and container builds report a
  meaningful version instead of `dev`.
- Graceful shutdown: the server drains in-flight requests on `SIGINT`/`SIGTERM`.

### Changed
- Modernized the reverse proxy for Go 1.26 and refactored it for clarity
  (shared path lists, small focused helpers, less duplication).
- Bumped dependencies (`golang.org/x/crypto`, `golang.org/x/sys`,
  `github.com/ProtonMail/go-crypto`).

### Fixed
- Data race on the shared transport's original-path field (now carried through
  the request context).
- Request bodies were forwarded with chunked encoding, which upstreams that only
  read `Content-Length` saw as empty; bodies are now sent with explicit length.

## [0.1.7] - 2025-09-05

- Last tagged release prior to the 0.2.0 refactor. See the GitHub releases page
  for details of earlier versions.
