// Package proxy provides a reverse proxy for the Ollama API.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// SetVerbose enables per-request body and rewrite logging.
var Verbose atomic.Bool

// defaultMaxRedirects matches Go's standard client default.
const defaultMaxRedirects = 10

var (
	// modelBodyPaths are endpoints whose JSON body carries a model/name field we normalize.
	modelBodyPaths = []string{
		"/api/run", "/api/chat", "/api/generate", "/api/delete", "/api/show",
		"/api/chat/completions", "/v1/chat/completions", "/v1/completions",
	}
	// pullablePaths are endpoints we replay after auto-pulling a missing model.
	pullablePaths = []string{
		"/api/run", "/api/generate",
		"/api/chat/completions", "/v1/chat/completions", "/v1/completions",
	}
)

// origPathKey carries the client-facing request path (pre-rewrite) through the
// request context so the transport can log it without sharing mutable state.
type origPathKey struct{}

// hasPathSuffix reports whether path ends with any of the given suffixes.
func hasPathSuffix(path string, suffixes []string) bool {
	return slices.ContainsFunc(suffixes, func(s string) bool {
		return strings.HasSuffix(path, s)
	})
}

// bearer formats an Authorization header value for the given API key.
func bearer(apiKey string) string { return "Bearer " + apiKey }

// copyHeader copies all header values from src into dst.
func copyHeader(dst, src http.Header) {
	for key, vals := range src {
		dst[key] = vals
	}
}

// normalizeTarget ensures the target URL has a scheme and returns it parsed.
func normalizeTarget(targetURL string) (*url.URL, error) {
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "http://" + targetURL
	}
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}
	return target, nil
}

// OllamaProxy represents a proxy server for the Ollama API.
type OllamaProxy struct {
	host      string
	port      string
	targetURL *url.URL
	apiKey    string
	upstream  UpstreamKind
	server    *http.Server
}

// NewOllamaProxy creates a new Ollama proxy. host is the interface address to
// bind to (e.g. "127.0.0.1" for localhost-only, "0.0.0.0" for all interfaces).
// upstream is the detected upstream flavor (see DetectUpstream); pass
// UpstreamUnknown to keep the default Ollama-compatible behavior.
func NewOllamaProxy(host, port, targetURL, apiKey string, upstream UpstreamKind) (*OllamaProxy, error) {
	hadScheme := strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://")
	target, err := normalizeTarget(targetURL)
	if err != nil {
		return nil, err
	}
	if !hadScheme {
		log.Printf("No protocol specified in API_ENDPOINT, using: %s", target)
	}

	return &OllamaProxy{
		host:      host,
		port:      port,
		targetURL: target,
		apiKey:    apiKey,
		upstream:  upstream,
	}, nil
}

// shutdownTimeout bounds how long Start waits for in-flight requests to drain.
const shutdownTimeout = 10 * time.Second

// Start starts the proxy server and blocks until ctx is cancelled or the server
// stops on its own. On cancellation it shuts down gracefully and returns nil.
func (p *OllamaProxy) Start(ctx context.Context) error {
	log.Printf("Starting Ollama proxy server on port %s", p.port)
	log.Printf("Forwarding requests to: %s", p.targetURL)
	if p.apiKey != "" {
		log.Print("API Key authentication enabled")
	}

	proxy := httputil.NewSingleHostReverseProxy(p.targetURL)

	// Wrap the default director to set the upstream host, inject auth, and
	// reconcile the request path with the target's base path.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalPath := req.URL.Path
		originalDirector(req)
		req.Host = p.targetURL.Host
		if p.apiKey != "" {
			req.Header.Set("Authorization", bearer(p.apiKey))
		}
		req.URL.Path = p.resolvePath(originalPath, req.URL.Path)
	}

	autoPull := p.upstream.SupportsPull()
	if !autoPull {
		log.Printf("Auto-pull disabled: %s upstreams don't support /api/pull", p.upstream)
	}
	proxy.Transport = newCustomTransport(defaultMaxRedirects, autoPull)

	addr := net.JoinHostPort(p.host, p.port)
	p.server = &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(p.handle(proxy)),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- p.server.ListenAndServe() }()

	log.Printf("Ollama proxy server listening on %s", addr)
	if p.host == "127.0.0.1" || p.host == "localhost" || p.host == "::1" {
		log.Print("Only local (loopback) connections are accepted; set HOST=0.0.0.0 (or --host 0.0.0.0) to allow external access")
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Print("Shutting down proxy server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return p.server.Shutdown(shutdownCtx)
	}
}

// handle returns the HTTP handler that normalizes request bodies, applies CORS,
// and forwards everything else through the reverse proxy.
func (p *OllamaProxy) handle(proxy *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Remember the client-facing path before the director rewrites it.
		r = r.WithContext(context.WithValue(r.Context(), origPathKey{}, r.URL.Path))

		rewriteRequestBody(r)

		// Answer CORS preflight requests directly.
		if r.Method == http.MethodOptions {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "*")
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")
		proxy.ServeHTTP(w, r)
	}
}

// resolvePath decides the upstream path for a request. It first applies
// upstream-aware routing for OpenAI-style endpoints (so a bare "/chat/completions"
// reaches "/api/chat/completions" on Open WebUI or "/v1/chat/completions" on
// vLLM/Ollama); when no routing rule applies it falls back to the "/ollama"
// reconciliation in rewritePath. directorPath is the path already produced by the
// default director.
func (p *OllamaProxy) resolvePath(originalPath, directorPath string) string {
	routed, ok := routeOpenAIPath(p.upstream, originalPath)
	if !ok {
		return p.rewritePath(originalPath, directorPath)
	}

	// Honor an explicit base path in API_ENDPOINT, but don't double a prefix the
	// endpoint already carries (e.g. API_ENDPOINT=.../v1 with a /v1 route).
	base := strings.TrimSuffix(p.targetURL.Path, "/")
	if base != "" && !strings.HasPrefix(routed, base+"/") && routed != base {
		routed = base + routed
	}
	final := strings.ReplaceAll(routed, "//", "/")
	if Verbose.Load() && final != originalPath {
		log.Printf("Routed %s -> %s for %s upstream", originalPath, final, p.upstream)
	}
	return final
}

// rewritePath reconciles the incoming path with the target's base path so the
// "/ollama" prefix is neither duplicated nor dropped. directorPath is the path
// already produced by the default director and is used as the fallback.
func (p *OllamaProxy) rewritePath(originalPath, directorPath string) string {
	targetPath := strings.TrimSuffix(p.targetURL.Path, "/")
	path := directorPath

	switch {
	case strings.HasPrefix(originalPath, "/ollama/"):
		switch {
		case strings.HasSuffix(targetPath, "/ollama"):
			// Target already ends with /ollama; drop the prefix to avoid duplication.
			path = strings.Replace(originalPath, "/ollama", "", 1)
		case targetPath == "":
			// Target has no base path; preserve the /ollama prefix as-is.
			path = originalPath
		}
	case !strings.HasPrefix(originalPath, "/ollama"):
		if strings.HasSuffix(targetPath, "/ollama") {
			// Target expects an /ollama prefix the request didn't provide.
			path = "/ollama" + originalPath
		}
	}

	return strings.ReplaceAll(path, "//", "/")
}

// Stop stops the proxy server.
func (p *OllamaProxy) Stop() error {
	if p.server != nil {
		return p.server.Close()
	}
	return nil
}

// rewriteRequestBody normalizes model names in request bodies for the endpoints
// that carry them, replacing r.Body with the processed bytes when changed.
func rewriteRequestBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		return
	}
	if !hasPathSuffix(r.URL.Path, modelBodyPaths) {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		// Restore the original body on read failure so the downstream transport
		// or reverse proxy can still attempt to forward the request. The body
		// content may be incomplete but leaving r.Body nil would break the proxy.
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return
	}

	processed, modified := processModelBody(r.URL.Path, bodyBytes)
	if !modified {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return
	}

	if Verbose.Load() {
		log.Printf("Final forwarded body for %s: %s", r.URL.Path, processed)
	}
	r.Body = io.NopCloser(bytes.NewReader(processed))
	r.ContentLength = int64(len(processed))
	r.Header.Set("Content-Length", strconv.Itoa(len(processed)))
}

// customTransport forwards requests with a redirect-following client, logs the
// exchange, and auto-pulls models that the upstream reports as missing.
type customTransport struct {
	client       *http.Client
	maxRedirects int
	autoPull     bool // attempt Ollama /api/pull when a model is missing
}

func newCustomTransport(maxRedirects int, autoPull bool) *customTransport {
	t := &customTransport{maxRedirects: maxRedirects, autoPull: autoPull}
	t.client = &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 {
				log.Printf("Following redirect: %s -> %s", via[len(via)-1].URL, req.URL)
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			// Carry the original headers across the redirect, except those the
			// stdlib manages per-hop.
			for key, vals := range via[0].Header {
				switch key {
				case "Authorization", "User-Agent", "Content-Length":
					continue
				}
				req.Header[key] = vals
			}
			return nil
		},
	}
	return t
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rebuild as a client request (drops RequestURI, which clients may not set).
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("creating proxied request: %w", err)
	}
	copyHeader(newReq.Header, req.Header)
	// Preserve the known length: NewRequest can't infer it from a NopCloser body,
	// and a zero ContentLength would force chunked encoding on every request.
	newReq.ContentLength = req.ContentLength

	// Buffer the body for endpoints we may need to replay after an auto-pull.
	// Skipped entirely when auto-pull is off (e.g. vLLM), which also disables the
	// missing-model retry path below since originalBody stays nil.
	var originalBody []byte
	if t.autoPull && req.Body != nil && req.Method == http.MethodPost && hasPathSuffix(req.URL.Path, pullablePaths) {
		originalBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		req.Body.Close()
		newReq.Body = io.NopCloser(bytes.NewReader(originalBody))
		newReq.ContentLength = int64(len(originalBody))
	}

	resp, err := t.client.Do(newReq)
	if err != nil {
		return nil, err
	}

	t.logExchange(req, resp)

	switch {
	case resp.StatusCode == http.StatusBadRequest && originalBody != nil &&
		hasPathSuffix(req.URL.Path, pullablePaths):
		return t.handleMissingModel(req, resp, originalBody)

	case resp.StatusCode != http.StatusOK:
		// Buffer and log non-OK bodies, then restore them for downstream readers.
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading error response body: %v", err)
		} else {
			log.Printf("ERROR RESPONSE (%d): %s", resp.StatusCode, respBody)
		}
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}

	return resp, nil
}

// logExchange logs a single request/response line, suppressing HEAD unless Verbose.
func (t *customTransport) logExchange(req *http.Request, resp *http.Response) {
	if req.Method == http.MethodHead && !Verbose.Load() {
		return
	}
	orig, _ := req.Context().Value(origPathKey{}).(string)
	if orig == "" {
		orig = req.URL.Path
	}
	log.Printf("%s %s -> %s://%s%s -> %d",
		req.Method, orig, req.URL.Scheme, req.URL.Host, req.URL.Path, resp.StatusCode)
}

// handleMissingModel inspects a 400 response and, if it indicates a missing
// model, pulls the model and replays the original request. On any failure it
// restores and returns the original error response.
func (t *customTransport) handleMissingModel(req *http.Request, resp *http.Response, originalBody []byte) (*http.Response, error) {
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()

	// restore returns the original error response with a re-readable body.
	restore := func() (*http.Response, error) {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return resp, nil
	}

	if err != nil {
		log.Printf("Error reading error response body: %v", err)
		return restore()
	}
	if !isModelNotFound(respBody) {
		return restore()
	}

	modelName := modelNameFromBody(originalBody)
	if modelName == "" {
		return restore()
	}

	log.Printf("Model %q not found. Attempting to pull it first...", modelName)
	if err := t.pullModel(req, modelName); err != nil {
		log.Printf("Model pull failed: %v", err)
		return restore()
	}

	log.Printf("Successfully pulled model %q. Retrying original request...", modelName)
	retryReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(originalBody))
	if err != nil {
		log.Printf("Error recreating original request: %v", err)
		return restore()
	}
	copyHeader(retryReq.Header, req.Header)

	retryResp, err := t.client.Do(retryReq)
	if err != nil {
		log.Printf("Error during retry request: %v", err)
		return restore()
	}
	log.Printf("Retry request completed with status %d", retryResp.StatusCode)
	return retryResp, nil
}

// pullModel issues a blocking /api/pull for the given model on the upstream.
func (t *customTransport) pullModel(req *http.Request, modelName string) error {
	pullURL := fmt.Sprintf("%s://%s/api/pull", req.URL.Scheme, req.URL.Host)
	pullJSON, _ := json.Marshal(map[string]any{"name": modelName})

	pullReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, pullURL, bytes.NewReader(pullJSON))
	if err != nil {
		return fmt.Errorf("creating pull request: %w", err)
	}
	pullReq.Header.Set("Content-Type", "application/json")
	if auth := req.Header.Get("Authorization"); auth != "" {
		pullReq.Header.Set("Authorization", auth)
	}

	log.Printf("Sending pull request for model: %s", modelName)
	pullResp, err := t.client.Do(pullReq)
	if err != nil {
		return fmt.Errorf("executing pull request: %w", err)
	}
	defer pullResp.Body.Close()

	if pullResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pullResp.Body)
		return fmt.Errorf("pull returned status %d: %s", pullResp.StatusCode, body)
	}

	// Drain the body so the pull fully completes before we retry.
	if _, err := io.Copy(io.Discard, pullResp.Body); err != nil {
		return fmt.Errorf("draining pull response: %w", err)
	}
	return nil
}

// isModelNotFound reports whether an upstream error body describes a missing model.
// It checks both "error" (Ollama native) and "detail" (OpenAI-compatible) fields.
func isModelNotFound(respBody []byte) bool {
	var errResp map[string]any
	if json.Unmarshal(respBody, &errResp) != nil {
		return false
	}

	msg, _ := errResp["error"].(string)
	if msg == "" {
		msg, _ = errResp["detail"].(string)
	}
	if msg == "" {
		return false
	}

	msg = strings.ToLower(msg)
	return strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "no model") ||
		(strings.Contains(msg, "model") && strings.Contains(msg, "not found"))
}

// modelNameFromBody extracts a model name from a request body, preferring the
// "model" field and falling back to "name".
func modelNameFromBody(body []byte) string {
	var bodyJSON map[string]any
	if json.Unmarshal(body, &bodyJSON) != nil {
		return ""
	}
	if model, ok := bodyJSON["model"].(string); ok {
		return model
	}
	if name, ok := bodyJSON["name"].(string); ok {
		return name
	}
	return ""
}

// addDefaultTagToModel adds ":latest" to model names that don't have a tag.
func addDefaultTagToModel(modelName string) string {
	if modelName != "" && !strings.Contains(modelName, ":") {
		return modelName + ":latest"
	}
	return modelName
}

// stripOpenAIPrefix removes the "openai/" prefix from model names if present.
func stripOpenAIPrefix(modelName string) string {
	return strings.TrimPrefix(modelName, "openai/")
}

// processModelBody parses and mutates an Ollama JSON request body. It ensures
// model/name fields have a default :latest tag (except on OpenAI-compatible
// endpoints) and, for /api/show, keeps name and model in sync so upstreams that
// expect either will work. It returns the (possibly modified) bytes and whether
// any change was made.
func processModelBody(path string, body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var bodyJSON map[string]any
	if json.Unmarshal(body, &bodyJSON) != nil {
		return body, false
	}

	// OpenAI-compatible endpoints must not receive a :latest tag.
	isOpenAIEndpoint := strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/completions")
	modified := false

	updateField := func(field string) {
		v, ok := bodyJSON[field].(string)
		if !ok || v == "" {
			return
		}

		updated := stripOpenAIPrefix(v)
		if updated != v && Verbose.Load() {
			log.Printf("Stripped openai/ prefix from model (%s field): %s -> %s", field, v, updated)
		}
		if !isOpenAIEndpoint {
			tagged := addDefaultTagToModel(updated)
			if tagged != updated && Verbose.Load() {
				log.Printf("Updated model in body (%s field): %s -> %s", field, updated, tagged)
			}
			updated = tagged
		}

		if updated != v {
			bodyJSON[field] = updated
			modified = true
		}
	}

	updateField("name")
	updateField("model")

	if strings.HasSuffix(path, "/api/show") {
		modified = syncShowNameModel(bodyJSON) || modified
	}

	if !modified {
		return body, false
	}
	newBytes, err := json.Marshal(bodyJSON)
	if err != nil {
		return body, false
	}
	return newBytes, true
}

// syncShowNameModel ensures /api/show requests carry both "name" and "model"
// with identical values (preferring "name"). It reports whether it changed body.
func syncShowNameModel(body map[string]any) bool {
	nameVal, hasName := body["name"].(string)
	modelVal, hasModel := body["model"].(string)

	switch {
	case hasName && !hasModel:
		body["model"] = nameVal
		if Verbose.Load() {
			log.Printf("Added missing 'model' field for /api/show using name=%s", nameVal)
		}
		return true
	case hasModel && !hasName:
		body["name"] = modelVal
		if Verbose.Load() {
			log.Printf("Added missing 'name' field for /api/show using model=%s", modelVal)
		}
		return true
	case hasName && hasModel && nameVal != modelVal:
		body["model"] = nameVal // prefer name, overwrite model to match
		if Verbose.Load() {
			log.Printf("Normalized differing name/model for /api/show: name=%s model=%s", nameVal, modelVal)
		}
		return true
	}
	return false
}

// UpstreamKind identifies the flavor of API server goophy is proxying to, so it
// can adapt its behavior (e.g. only Ollama-style upstreams support auto-pull).
type UpstreamKind int

const (
	// UpstreamUnknown means the server is reachable but its flavor wasn't identified.
	UpstreamUnknown UpstreamKind = iota
	UpstreamOllama
	UpstreamVLLM
	UpstreamOpenWebUI
	UpstreamOpenAI // generic OpenAI-compatible server
)

func (k UpstreamKind) String() string {
	switch k {
	case UpstreamOllama:
		return "Ollama"
	case UpstreamVLLM:
		return "vLLM"
	case UpstreamOpenWebUI:
		return "Open WebUI"
	case UpstreamOpenAI:
		return "OpenAI-compatible"
	default:
		return "unknown"
	}
}

// SupportsPull reports whether this upstream understands Ollama's /api/pull, and
// therefore whether auto-pull-on-missing-model is worth attempting. Only positively
// identified OpenAI-only servers (vLLM, generic OpenAI) are excluded; everything
// else (Ollama, Open WebUI, or unknown) keeps the Ollama behavior to avoid
// regressing existing setups.
func (k UpstreamKind) SupportsPull() bool {
	switch k {
	case UpstreamVLLM, UpstreamOpenAI:
		return false
	default:
		return true
	}
}

// openAIPrefix returns the base path this upstream serves its OpenAI-compatible
// API under: "/v1" for vLLM/Ollama/generic OpenAI, "/api" for Open WebUI. It
// returns "" for unknown upstreams, meaning "don't rewrite — forward as-is".
func (k UpstreamKind) openAIPrefix() string {
	switch k {
	case UpstreamOpenWebUI:
		return "/api"
	case UpstreamVLLM, UpstreamOpenAI, UpstreamOllama:
		return "/v1"
	default:
		return ""
	}
}

// openAIStyleSuffixes are OpenAI-compatible endpoints whose required prefix differs
// per upstream. Ordered longest-first so "/chat/completions" is matched before the
// "/completions" that is its own suffix.
var openAIStyleSuffixes = []string{
	"/chat/completions",
	"/completions",
	"/embeddings",
	"/rerank",
	"/models",
}

// routeOpenAIPath rewrites an OpenAI-style request path to the prefix the detected
// upstream expects, so a client can point at goophy with a bare host (no /v1 or
// /api) and still reach the right endpoint. For example, with an Open WebUI
// upstream "/chat/completions" becomes "/api/chat/completions"; with vLLM or Ollama
// it becomes "/v1/chat/completions". It returns (path, false) when no rule applies
// — an unknown upstream, or a non-OpenAI path such as Ollama-native "/api/chat" —
// so the caller keeps the existing forwarding behavior.
func routeOpenAIPath(kind UpstreamKind, path string) (string, bool) {
	prefix := kind.openAIPrefix()
	if prefix == "" {
		return path, false
	}
	for _, suffix := range openAIStyleSuffixes {
		if strings.HasSuffix(path, suffix) {
			return prefix + suffix, true
		}
	}
	return path, false
}

// healthProbe pairs an endpoint path with the upstream flavor a 2xx from it hints
// at. Ordered most-specific first so a distinctive endpoint wins before we fall
// back to the generic "/health" liveness check.
type healthProbe struct {
	path string
	hint UpstreamKind
}

// healthProbes are tried in order to confirm reachability and identify the flavor.
// Ordering matters: distinctive endpoints come first so an ambiguous one (like
// /api/version, which BOTH Ollama and Open WebUI answer) rarely gets to decide.
// A probe that returns 2xx but can't be classified (UpstreamUnknown) doesn't stop
// the sweep — we keep going in case a later probe is decisive.
var healthProbes = []healthProbe{
	{"/", UpstreamUnknown},             // native Ollama returns the "Ollama is running" banner
	{"/api/config", UpstreamOpenWebUI}, // Open WebUI's public config; Ollama & vLLM 404 here
	{"/api/tags", UpstreamOllama},      // native Ollama (Open WebUI serves this only under /ollama)
	{"/v1/models", UpstreamOpenAI},     // vLLM / generic OpenAI (refined via headers+body)
	{"/api/models", UpstreamOpenWebUI}, // Open WebUI aggregated model list
	{"/api/version", UpstreamOllama},   // Ollama, but Open WebUI answers too — refined below
	{"/health", UpstreamUnknown},       // generic liveness; {"status":true} means Open WebUI
}

// looksLikePythonServer reports whether the Server header indicates a Python ASGI
// stack (uvicorn/starlette/hypercorn) — i.e. Open WebUI or vLLM, never native
// Ollama, which is written in Go. Best-effort only: a reverse proxy may overwrite
// this header, so it's used to disambiguate, not as a primary signal.
func looksLikePythonServer(server string) bool {
	return strings.Contains(server, "uvicorn") ||
		strings.Contains(server, "starlette") ||
		strings.Contains(server, "hypercorn")
}

// classifyUpstream refines a probe's hint using the response's Server header and
// body so we can tell vLLM apart from other OpenAI-compatible servers, and native
// Ollama apart from Open WebUI (which answers many of the same Ollama paths).
func classifyUpstream(hint UpstreamKind, path string, hdr http.Header, body []byte) UpstreamKind {
	server := strings.ToLower(hdr.Get("Server"))
	compact := strings.ReplaceAll(strings.ToLower(string(body)), " ", "")

	switch {
	case path == "/":
		// Native Ollama answers the root with the plain-text "Ollama is running".
		if strings.Contains(compact, "ollamaisrunning") {
			return UpstreamOllama
		}
		return UpstreamUnknown // e.g. Open WebUI serves its HTML app here — not decisive
	case strings.HasSuffix(path, "/api/config"):
		// Open WebUI's /api/config is a JSON object carrying its name/features.
		return UpstreamOpenWebUI
	case strings.HasSuffix(path, "/v1/models"):
		// vLLM tags its models owned_by:"vllm" and is fronted by uvicorn.
		if strings.Contains(compact, `"owned_by":"vllm"`) || looksLikePythonServer(server) {
			return UpstreamVLLM
		}
		return UpstreamOpenAI
	case strings.HasSuffix(path, "/api/tags"), strings.HasSuffix(path, "/api/version"):
		// Both native Ollama and Open WebUI answer these. Native Ollama (Go) sends
		// no uvicorn/Python Server header; a Python one means it's Open WebUI.
		if looksLikePythonServer(server) {
			return UpstreamOpenWebUI
		}
		return UpstreamOllama
	case strings.HasSuffix(path, "/health"):
		// Open WebUI's /health returns {"status":true}; llama.cpp returns {"status":"ok"}.
		if strings.Contains(compact, `"status":true`) {
			return UpstreamOpenWebUI
		}
		return UpstreamUnknown
	}
	return hint
}

// DetectUpstream checks that the upstream endpoint is reachable and identifies its
// flavor (Ollama, vLLM, Open WebUI, or generic OpenAI-compatible). It supports both
// Ollama and OpenAI-compatible servers by probing several well-known endpoints and
// returns as soon as one responds. It returns a descriptive error only if every
// probe fails to connect. The requests are bounded by ctx, so a cancelled ctx
// aborts the check.
func DetectUpstream(ctx context.Context, targetURL, apiKey string) (UpstreamKind, error) {
	target, err := normalizeTarget(targetURL)
	if err != nil {
		return UpstreamUnknown, err
	}

	client := &http.Client{Timeout: 10 * time.Second}

	var (
		lastConnErr error  // last transport-level failure (server unreachable)
		reachable   bool   // saw at least one HTTP response
		sawAuth     bool   // some probe returned 401/403 (reachable, key needed)
		lastStatus  int    // last non-2xx status from a reachable server
		lastBody    []byte // body accompanying lastStatus
	)

	for _, probe := range healthProbes {
		probeURL := fmt.Sprintf("%s://%s%s", target.Scheme, target.Host, probe.path)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if err != nil {
			return UpstreamUnknown, fmt.Errorf("creating request: %w", err)
		}
		if apiKey != "" {
			req.Header.Set("Authorization", bearer(apiKey))
		}

		resp, err := client.Do(req)
		if err != nil {
			// Abort immediately if the caller cancelled; don't mask it as unreachable.
			if ctx.Err() != nil {
				return UpstreamUnknown, err
			}
			lastConnErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		reachable = true

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// A 2xx that we can't classify (e.g. Open WebUI's HTML root) doesn't
			// end the sweep — keep probing for a decisive endpoint.
			if kind := classifyUpstream(probe.hint, probe.path, resp.Header, body); kind != UpstreamUnknown {
				if server := resp.Header.Get("Server"); server != "" {
					log.Printf("Detected upstream: %s (%s responded %d, Server: %s)", kind, probe.path, resp.StatusCode, server)
				} else {
					log.Printf("Detected upstream: %s (%s responded %d)", kind, probe.path, resp.StatusCode)
				}
				return kind, nil
			}
			lastStatus, lastBody = resp.StatusCode, body
			continue
		}

		// Non-2xx: the endpoint is reachable but this isn't its path (404) or it
		// wants auth (401/403). Either way, keep probing — a later public endpoint
		// may still identify the flavor.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			sawAuth = true
		}
		lastStatus, lastBody = resp.StatusCode, body
	}

	// Every probe got an HTTP response but none was a clean 2xx: the server is up
	// but no known endpoint identified it. Treat it as reachable so we don't block
	// startup against an unusual deployment.
	if reachable {
		if sawAuth {
			log.Printf("Warning: endpoint reachable but every probe 404'd or needed auth (last status %d); flavor unknown — check API_KEY", lastStatus)
		} else {
			log.Printf("Warning: endpoint reachable but flavor unknown (last status %d: %s)", lastStatus, lastBody)
		}
		return UpstreamUnknown, nil
	}

	return UpstreamUnknown, fmt.Errorf("connecting to endpoint %s://%s: %w", target.Scheme, target.Host, lastConnErr)
}
