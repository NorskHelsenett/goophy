// Package proxy provides a reverse proxy for the Ollama API.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"sync"
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
		// Suffix match, so these also cover the /v1 and /api prefixed forms and the
		// bare paths that routeOpenAIPath only prefixes after the body is rewritten.
		"/chat/completions", "/completions",
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

// upstreamKindKey carries the upstream flavor resolved for a request, so the
// director and transport agree on it even if detection completes mid-request.
type upstreamKindKey struct{}

// upstreamKindFrom returns the upstream flavor stored in ctx, or UpstreamUnknown.
func upstreamKindFrom(ctx context.Context) UpstreamKind {
	kind, _ := ctx.Value(upstreamKindKey{}).(UpstreamKind)
	return kind
}

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
	server    *http.Server

	// upstream holds the detected UpstreamKind. While it's still unknown because
	// detection failed (e.g. the upstream wasn't up at startup), requests retry it
	// at most once per redetectInterval. detectMu guards lastDetect and settled.
	upstream   atomic.Int32
	detectMu   sync.Mutex
	lastDetect time.Time
	settled    bool // a retry reached the upstream; unknown is now final
}

const (
	// redetectInterval throttles detection retries while the upstream is unknown.
	redetectInterval = 30 * time.Second
	// redetectTimeout bounds a detection retry, which blocks the triggering request.
	redetectTimeout = 5 * time.Second
)

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

	p := &OllamaProxy{
		host:       host,
		port:       port,
		targetURL:  target,
		apiKey:     apiKey,
		lastDetect: time.Now(), // the caller just ran detection
	}
	p.upstream.Store(int32(upstream))
	logUpstreamBehavior(upstream)
	return p, nil
}

// logUpstreamBehavior logs the behavior changes implied by a detected flavor.
func logUpstreamBehavior(kind UpstreamKind) {
	if kind != UpstreamUnknown && !kind.SupportsPull() {
		log.Printf("Auto-pull disabled: %s upstreams don't support /api/pull", kind)
	}
}

// upstreamKind returns the detected upstream flavor. While it's still unknown it
// retries detection (throttled, and skipped if another request is already
// detecting), so an upstream that comes up after goophy is still identified.
func (p *OllamaProxy) upstreamKind() UpstreamKind {
	if kind := UpstreamKind(p.upstream.Load()); kind != UpstreamUnknown {
		return kind
	}
	if !p.detectMu.TryLock() {
		return UpstreamUnknown
	}
	defer p.detectMu.Unlock()
	// Re-check: another request may have finished detecting while we waited.
	if kind := UpstreamKind(p.upstream.Load()); kind != UpstreamUnknown {
		return kind
	}
	if p.settled || time.Since(p.lastDetect) < redetectInterval {
		return UpstreamUnknown
	}
	p.lastDetect = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), redetectTimeout)
	defer cancel()
	kind, err := DetectUpstream(ctx, p.targetURL.String(), p.apiKey)
	if err != nil {
		log.Printf("Upstream detection retry failed: %v", err)
		return UpstreamUnknown
	}
	// Reachable: whatever we found is the answer, recognized or not.
	p.settled = true
	if kind != UpstreamUnknown {
		p.upstream.Store(int32(kind))
		logUpstreamBehavior(kind)
	}
	return kind
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

	addr := net.JoinHostPort(p.host, p.port)
	p.server = &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(p.handle(p.newReverseProxy())),
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

// newReverseProxy builds the reverse proxy for the configured target: a director
// that sets the upstream host, injects auth, and routes the path (see resolvePath),
// plus a transport whose auto-pull behavior follows the request's upstream flavor.
func (p *OllamaProxy) newReverseProxy() *httputil.ReverseProxy {
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
		req.URL.Path = p.resolvePath(upstreamKindFrom(req.Context()), originalPath, req.URL.Path)
	}

	proxy.Transport = newCustomTransport(defaultMaxRedirects)
	return proxy
}

// handle returns the HTTP handler that normalizes request bodies, applies CORS,
// and forwards everything else through the reverse proxy.
func (p *OllamaProxy) handle(proxy *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Remember the client-facing path before the director rewrites it, and pin
		// the upstream flavor so routing and auto-pull agree for this request.
		ctx := context.WithValue(r.Context(), origPathKey{}, r.URL.Path)
		ctx = context.WithValue(ctx, upstreamKindKey{}, p.upstreamKind())
		r = r.WithContext(ctx)

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
func (p *OllamaProxy) resolvePath(kind UpstreamKind, originalPath, directorPath string) string {
	routed, ok := routeOpenAIPath(kind, originalPath)
	if !ok {
		return p.rewritePath(originalPath, directorPath)
	}

	// Honor an explicit base path in API_ENDPOINT. A base that already ends in an
	// OpenAI prefix (".../v1" or ".../api") is the operator naming the prefix, so
	// it replaces the detected one rather than being stacked on top of it; any
	// other base (e.g. "/gateway", "/ollama") is a mount point the prefix goes under.
	base := strings.TrimSuffix(p.targetURL.Path, "/")
	switch root, prefix := splitOpenAIPrefix(base); {
	case prefix != "":
		routed = root + prefix + strings.TrimPrefix(routed, kind.openAIPrefix())
	case base != "":
		routed = base + routed
	}
	final := strings.ReplaceAll(routed, "//", "/")
	if Verbose.Load() && final != originalPath {
		log.Printf("Routed %s -> %s for %s upstream", originalPath, final, kind)
	}
	return final
}

// splitOpenAIPrefix splits a base path ending in a known OpenAI prefix ("/v1" or
// "/api") into the mount point before it and the prefix itself. For any other
// base it returns (base, "").
func splitOpenAIPrefix(base string) (root, prefix string) {
	for _, pfx := range []string{"/v1", "/api"} {
		if strings.HasSuffix(base, pfx) {
			return strings.TrimSuffix(base, pfx), pfx
		}
	}
	return base, ""
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
		// The read failed partway. Forward what we managed to read so the request
		// isn't dropped, but sync ContentLength to the (possibly truncated) body:
		// leaving the original larger length would make the transport abort with a
		// ContentLength/body mismatch instead of forwarding.
		setRequestBody(r, bodyBytes)
		return
	}

	processed, modified := processModelBody(r.URL.Path, bodyBytes)
	if !modified {
		setRequestBody(r, bodyBytes)
		return
	}

	if Verbose.Load() {
		log.Printf("Final forwarded body for %s: %s", r.URL.Path, processed)
	}
	setRequestBody(r, processed)
}

// setRequestBody replaces r.Body with b, keeping ContentLength and the
// Content-Length header consistent with it so the transport never sees a
// length/body mismatch.
func setRequestBody(r *http.Request, b []byte) {
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	r.Header.Set("Content-Length", strconv.Itoa(len(b)))
}

// customTransport forwards requests with a redirect-following client, logs the
// exchange, and auto-pulls models that the upstream reports as missing.
type customTransport struct {
	client       *http.Client
	maxRedirects int
}

func newCustomTransport(maxRedirects int) *customTransport {
	t := &customTransport{maxRedirects: maxRedirects}
	t.client = &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && Verbose.Load() {
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
	// Skipped entirely when the upstream can't pull (e.g. vLLM), which also
	// disables the missing-model retry path below since originalBody stays nil.
	autoPull := upstreamKindFrom(req.Context()).SupportsPull()
	var originalBody []byte
	if autoPull && req.Body != nil && req.Method == http.MethodPost && hasPathSuffix(req.URL.Path, pullablePaths) {
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

// SupportsPull reports whether this upstream understands Ollama's /api/pull at the
// host root, and therefore whether auto-pull-on-missing-model is worth attempting.
// Positively identified non-Ollama servers are excluded: vLLM and generic OpenAI
// can't pull, and Open WebUI only exposes pulling under /ollama/api/pull. Ollama
// and unknown upstreams keep the Ollama behavior to avoid regressing existing setups.
func (k UpstreamKind) SupportsPull() bool {
	switch k {
	case UpstreamVLLM, UpstreamOpenAI, UpstreamOpenWebUI:
		return false
	default:
		return true
	}
}

// openAIPrefix returns the base path this upstream serves its OpenAI-compatible
// API under: "/api" for Open WebUI, "/v1" for everyone else. Unknown upstreams
// — unreachable at startup, or reachable but unidentified — fall back to "/v1",
// the universal OpenAI prefix and Ollama's own, so a client's bare
// "/chat/completions" still routes correctly for the common Ollama case instead
// of being forwarded verbatim and 404ing.
func (k UpstreamKind) openAIPrefix() string {
	if k == UpstreamOpenWebUI {
		return "/api"
	}
	return "/v1"
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
// upstream "/chat/completions" becomes "/api/chat/completions"; with vLLM, Ollama,
// or an as-yet-unidentified upstream it becomes "/v1/chat/completions". It returns
// (path, false) for a non-OpenAI path such as Ollama-native "/api/chat", so the
// caller keeps the existing "/ollama"-reconciliation forwarding behavior.
func routeOpenAIPath(kind UpstreamKind, path string) (string, bool) {
	prefix := kind.openAIPrefix()
	for _, suffix := range openAIStyleSuffixes {
		if !strings.HasSuffix(path, suffix) {
			continue
		}
		// Only route a path that is the bare endpoint ("/embeddings") or already
		// carries a recognized OpenAI prefix ("/v1/embeddings", or this upstream's
		// own prefix). Anything else — notably an Ollama-native "/api/embeddings"
		// or a client "/ollama/..." prefix — merely shares the suffix and must be
		// left for rewritePath, so we don't strip its prefix or mangle its body.
		head := strings.TrimSuffix(path, suffix)
		if head == "" || head == "/v1" || head == prefix {
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
	// bodyContains reports whether the response body holds needle, ignoring ASCII
	// case and spaces. Computed lazily so probes that decide on path or header
	// alone (e.g. Open WebUI's large HTML root) don't pay to transform the body.
	bodyContains := func(needle string) bool {
		compact := strings.ReplaceAll(strings.ToLower(string(body)), " ", "")
		return strings.Contains(compact, needle)
	}
	// jsonWithKey reports whether the body looks like a JSON object carrying key.
	// It guards the JSON endpoints against catch-all hosts (SPA servers, gateways)
	// that answer every path with 200 and an HTML page. A substring check rather
	// than a full parse, so a body truncated at maxProbeBody still classifies.
	jsonWithKey := func(key string) bool {
		return bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) && bodyContains(`"`+key+`"`)
	}

	switch {
	case path == "/":
		// Native Ollama answers the root with the plain-text "Ollama is running".
		if bodyContains("ollamaisrunning") {
			return UpstreamOllama
		}
		return UpstreamUnknown // e.g. Open WebUI serves its HTML app here — not decisive
	case strings.HasSuffix(path, "/api/config"):
		// Open WebUI's /api/config is a JSON object carrying its name/features.
		if !jsonWithKey("name") {
			return UpstreamUnknown
		}
		return UpstreamOpenWebUI
	case strings.HasSuffix(path, "/v1/models"), strings.HasSuffix(path, "/api/models"):
		// Both are OpenAI-style model lists: {"object":"list","data":[...]}.
		if !jsonWithKey("data") {
			return UpstreamUnknown
		}
		if strings.HasSuffix(path, "/api/models") {
			return UpstreamOpenWebUI
		}
		// vLLM tags its models owned_by:"vllm" and is fronted by uvicorn.
		if bodyContains(`"owned_by":"vllm"`) || looksLikePythonServer(server) {
			return UpstreamVLLM
		}
		return UpstreamOpenAI
	case strings.HasSuffix(path, "/api/tags"), strings.HasSuffix(path, "/api/version"):
		// Ollama answers {"models":[...]} and {"version":"..."} respectively.
		if !jsonWithKey("models") && !jsonWithKey("version") {
			return UpstreamUnknown
		}
		// Both native Ollama and Open WebUI answer these. Native Ollama (Go) sends
		// no uvicorn/Python Server header; a Python one means it's Open WebUI.
		if looksLikePythonServer(server) {
			return UpstreamOpenWebUI
		}
		return UpstreamOllama
	case strings.HasSuffix(path, "/health"):
		// Open WebUI's /health returns {"status":true}; llama.cpp returns {"status":"ok"}.
		if bodyContains(`"status":true`) {
			return UpstreamOpenWebUI
		}
		return UpstreamUnknown
	}
	return hint
}

// maxProbeBody caps how much of a probe response is read: enough to classify the
// JSON endpoints, without buffering a large HTML app shell.
const maxProbeBody = 64 << 10

// maxLoggedProbeBody caps how much of an unrecognized probe body is logged.
const maxLoggedProbeBody = 200

// ErrUpstreamAuth means the upstream is reachable but rejected every probe with
// 401/403 — almost always a missing or wrong API_KEY. Unlike an unreachable
// upstream (which may simply come up after goophy), this is a configuration error
// the caller should surface fatally rather than start and fail every request.
var ErrUpstreamAuth = errors.New("upstream rejected authentication; check API_KEY")

// DetectUpstream checks that the upstream endpoint is reachable and identifies its
// flavor (Ollama, vLLM, Open WebUI, or generic OpenAI-compatible). It supports both
// Ollama and OpenAI-compatible servers by probing several well-known endpoints and
// returns as soon as one is classified. It returns an error if the upstream can't
// be reached (the sweep stops at the first connection failure before any response,
// so a black-holed host costs one timeout rather than one per probe), or
// ErrUpstreamAuth if every response was 401/403. The requests are bounded by ctx,
// so a cancelled ctx aborts the check.
func DetectUpstream(ctx context.Context, targetURL, apiKey string) (UpstreamKind, error) {
	target, err := normalizeTarget(targetURL)
	if err != nil {
		return UpstreamUnknown, err
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Honor a base path in API_ENDPOINT (e.g. http://gw/ollama) so probes hit the
	// upstream where it's actually served rather than host-root paths that 404. A
	// trailing OpenAI prefix (".../v1", ".../api") is dropped first, since the
	// probe paths carry their own (otherwise we'd probe /api/api/config).
	base, _ := splitOpenAIPrefix(strings.TrimSuffix(target.Path, "/"))

	var (
		responses  int    // probes that got an HTTP response
		authDenied int    // of those, how many were 401/403
		lastStatus int    // last status that didn't classify the upstream
		lastBody   []byte // body accompanying lastStatus
	)

	for _, probe := range healthProbes {
		probeURL := fmt.Sprintf("%s://%s%s%s", target.Scheme, target.Host, base, probe.path)

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
			// Nothing has answered yet, so the host itself is unreachable; the
			// remaining probes would only fail (or time out) the same way.
			if responses == 0 {
				return UpstreamUnknown, fmt.Errorf("connecting to endpoint %s://%s: %w", target.Scheme, target.Host, err)
			}
			continue // the host answered before; just this probe failed
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
		resp.Body.Close()
		responses++

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
			authDenied++
		}
		lastStatus, lastBody = resp.StatusCode, body
	}

	// Every response was 401/403: the server is up and the key is missing or
	// wrong. Fail fast so the operator sees it at startup instead of every
	// proxied request returning 401 later. A mix (e.g. vLLM with --api-key, which
	// guards only /v1 and answers /health openly) isn't conclusive: clients may
	// bring their own key, so that falls through to the warning below.
	if responses > 0 && authDenied == responses {
		return UpstreamUnknown, fmt.Errorf("endpoint %s://%s reachable but every probe returned 401/403 (last status %d): %w",
			target.Scheme, target.Host, lastStatus, ErrUpstreamAuth)
	}

	// Reachable but unrecognized (e.g. all 404): an unusual deployment. Start
	// anyway so we don't block against it.
	if len(lastBody) > maxLoggedProbeBody {
		lastBody = append(lastBody[:maxLoggedProbeBody:maxLoggedProbeBody], "..."...)
	}
	log.Printf("Warning: endpoint reachable but flavor unknown (last status %d: %q)", lastStatus, lastBody)
	return UpstreamUnknown, nil
}
