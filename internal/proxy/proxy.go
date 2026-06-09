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
	"time"
)

// Verbose enables per-request body and rewrite logging.
var Verbose bool

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
	server    *http.Server
}

// NewOllamaProxy creates a new Ollama proxy. host is the interface address to
// bind to (e.g. "127.0.0.1" for localhost-only, "0.0.0.0" for all interfaces).
func NewOllamaProxy(host, port, targetURL, apiKey string) (*OllamaProxy, error) {
	hadScheme := strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://")
	target, err := normalizeTarget(targetURL)
	if err != nil {
		return nil, err
	}
	if !hadScheme {
		log.Printf("No protocol specified in OLLAMA_ENDPOINT, using: %s", target)
	}

	return &OllamaProxy{
		host:      host,
		port:      port,
		targetURL: target,
		apiKey:    apiKey,
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
		req.URL.Path = p.rewritePath(originalPath, req.URL.Path)
	}

	proxy.Transport = newCustomTransport(defaultMaxRedirects)

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
		return
	}

	processed, modified := processModelBody(r.URL.Path, bodyBytes)
	if !modified {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return
	}

	if Verbose {
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
}

func newCustomTransport(maxRedirects int) *customTransport {
	t := &customTransport{maxRedirects: maxRedirects}
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
	var originalBody []byte
	if req.Body != nil && req.Method == http.MethodPost && hasPathSuffix(req.URL.Path, pullablePaths) {
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
	if req.Method == http.MethodHead && !Verbose {
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
		if updated != v && Verbose {
			log.Printf("Stripped openai/ prefix from model (%s field): %s -> %s", field, v, updated)
		}
		if !isOpenAIEndpoint {
			tagged := addDefaultTagToModel(updated)
			if tagged != updated && Verbose {
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
		if Verbose {
			log.Printf("Added missing 'model' field for /api/show using name=%s", nameVal)
		}
		return true
	case hasModel && !hasName:
		body["name"] = modelVal
		if Verbose {
			log.Printf("Added missing 'name' field for /api/show using model=%s", modelVal)
		}
		return true
	case hasName && hasModel && nameVal != modelVal:
		body["model"] = nameVal // prefer name, overwrite model to match
		if Verbose {
			log.Printf("Normalized differing name/model for /api/show: name=%s model=%s", nameVal, modelVal)
		}
		return true
	}
	return false
}

// PingEndpoint checks that the Ollama endpoint is reachable by hitting /health.
// It returns nil on success and a descriptive error otherwise. The request is
// bounded by ctx, so a cancelled ctx aborts the check.
func PingEndpoint(ctx context.Context, targetURL, apiKey string) error {
	target, err := normalizeTarget(targetURL)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	healthURL := fmt.Sprintf("%s://%s/health", target.Scheme, target.Host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", bearer(apiKey))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Ollama endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error response from Ollama endpoint (status %d): %s", resp.StatusCode, body)
	}
	return nil
}
