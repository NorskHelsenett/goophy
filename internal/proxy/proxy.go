// Package proxy provides a reverse proxy for Ollama API
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

var Verbose bool

// OllamaProxy represents a proxy server for Ollama API
type OllamaProxy struct {
	port      string
	targetURL *url.URL
	apiKey    string
	server    *http.Server
}

// NewOllamaProxy creates a new Ollama proxy
func NewOllamaProxy(port, targetURL, apiKey string) (*OllamaProxy, error) {
	// Add http:// prefix if no protocol is specified
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "http://" + targetURL
		log.Printf("No protocol specified in OLLAMA_ENDPOINT, using: %s", targetURL)
	}

	// Parse the target URL
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %v", err)
	}

	return &OllamaProxy{
		port:      port,
		targetURL: target,
		apiKey:    apiKey,
	}, nil
}

// Start starts the proxy server
func (p *OllamaProxy) Start() error {
	// Log configuration
	log.Printf("Starting Ollama proxy server on port %s", p.port)
	log.Printf("Forwarding requests to: %s", p.targetURL.String())
	if p.apiKey != "" {
		log.Print("API Key authentication enabled")
	}

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(p.targetURL)

	// Customize the director function to add our authorization header and fix path issues
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Store original path before the original director modifies it
		originalPath := req.URL.Path

		// Call the original director
		originalDirector(req)

		// Set the Host header to the target host
		req.Host = p.targetURL.Host

		// Add the Authorization header if API key is set
		if p.apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.apiKey))
		}

		// Fix path handling issues
		// First, normalize paths by removing trailing slashes
		targetPath := strings.TrimSuffix(p.targetURL.Path, "/")

		// Special handling for Ollama paths
		if strings.HasPrefix(originalPath, "/ollama/") {
			// Case 1: Request path starts with /ollama/
			if strings.HasSuffix(targetPath, "/ollama") {
				// If target already ends with /ollama, remove the /ollama prefix from request to prevent duplication
				req.URL.Path = strings.Replace(originalPath, "/ollama", "", 1)
			} else if targetPath == "" {
				// If target doesn't have a specific path, preserve the /ollama prefix
				req.URL.Path = originalPath
			}
		} else if strings.HasPrefix(originalPath, "/") && !strings.HasPrefix(originalPath, "/ollama") {
			// Case 2: Normal path without /ollama prefix
			if strings.HasSuffix(targetPath, "/ollama") {
				// If target has /ollama but request doesn't, add it
				req.URL.Path = "/ollama" + originalPath
			}
		}

		// Clean up any double slashes that might have been created
		req.URL.Path = strings.Replace(req.URL.Path, "//", "/", -1)

		// Log the final forwarded URL
		log.Printf("%s %s -> %s://%s%s",
			req.Method,
			originalPath,
			p.targetURL.Scheme,
			p.targetURL.Host,
			req.URL.Path)
	}

	// Create a custom transport that preserves the original request and modifies specific responses
	proxy.Transport = &customTransport{
		originalPath: "",
		maxRedirects: 10, // Default to 10 redirects, which is Go's default
	}

	// Create a handler function
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Store the original path for response processing
		if transport, ok := proxy.Transport.(*customTransport); ok {
			transport.originalPath = r.URL.Path
		}

		// For request bodies that may contain model names, read, modify and restore the body
		if (r.Method == "POST" || r.Method == "DELETE") && (strings.HasSuffix(r.URL.Path, "/api/run") ||
			strings.HasSuffix(r.URL.Path, "/api/chat") ||
			strings.HasSuffix(r.URL.Path, "/api/generate") ||
			strings.HasSuffix(r.URL.Path, "/api/delete") ||
			strings.HasSuffix(r.URL.Path, "/api/show")) {
			if r.Body != nil {
				// Read the body
				bodyBytes, err := io.ReadAll(r.Body)
				r.Body.Close()

				if err == nil {
					processed, modified := processModelBody(r.URL.Path, bodyBytes)
					if modified {
						if Verbose {
							log.Printf("Final forwarded body for %s: %s", r.URL.Path, string(processed))
						}
						// Always forward the processed body if it was modified
						r.Body = io.NopCloser(bytes.NewReader(processed))
						r.ContentLength = int64(len(processed))
						r.Header.Set("Content-Length", fmt.Sprintf("%d", len(processed)))
					} else {
						// Unmodified, restore original
						r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					}
				}
			}
		}

		// Handle preflight CORS requests
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Add CORS headers for all responses
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Use the proxy to serve the request
		proxy.ServeHTTP(w, r)
	}

	// Create the server
	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: http.HandlerFunc(handler),
	}

	// Start the server (this will block until the server is stopped)
	log.Printf("Ollama proxy server started at http://localhost:%s", p.port)
	return p.server.ListenAndServe()
}

// Stop stops the proxy server
func (p *OllamaProxy) Stop() error {
	if p.server != nil {
		return p.server.Close()
	}
	return nil
}

// Custom transport to preserve the original request and modify specific responses
type customTransport struct {
	originalPath string
	maxRedirects int // Maximum number of redirects to follow
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Create a redirect-enabled client for following redirects
	client := &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Log the redirect
			if len(via) > 0 {
				log.Printf("Following redirect: %s -> %s", via[len(via)-1].URL.String(), req.URL.String())
			}

			// Stop after maxRedirects
			if len(via) >= t.maxRedirects {
				return fmt.Errorf("stopped after %d redirects", t.maxRedirects)
			}

			// Copy headers from the original request
			for key, vals := range via[0].Header {
				// Skip headers that are set by the Go stdlib
				if key == "Authorization" || key == "User-Agent" || key == "Content-Length" {
					continue
				}
				req.Header[key] = vals
			}

			return nil
		},
	}

	// Create a clean request without the RequestURI field
	// which isn't allowed in client requests
	newReq, err := http.NewRequest(req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("error creating clean request: %w", err)
	}

	// Copy all headers from the original request
	for key, vals := range req.Header {
		newReq.Header[key] = vals
	}

	// Store original body for potential reuse
	var originalBody []byte
	if req.Body != nil && (req.Method == "POST" && (strings.HasSuffix(req.URL.Path, "/api/run") || strings.HasSuffix(req.URL.Path, "/api/generate"))) {
		// Read and store the body for model name extraction and request retry
		originalBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("error reading request body: %w", err)
		}
		req.Body.Close()

		// Replace the body for the current request
		newReq.Body = io.NopCloser(bytes.NewReader(originalBody))
		newReq.ContentLength = int64(len(originalBody))
		newReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(originalBody)))
	}

	// Execute the request using our client
	resp, err := client.Do(newReq)
	if err != nil {
		return nil, err
	}

	// Log the response status
	log.Printf("%s %s -> %d", req.Method, req.URL.Path, resp.StatusCode)

	// Handle case where model doesn't exist (for /api/run and /api/generate endpoints)
	if resp.StatusCode == http.StatusBadRequest &&
		(strings.HasSuffix(req.URL.Path, "/api/run") || strings.HasSuffix(req.URL.Path, "/api/generate")) &&
		originalBody != nil {

		// Read the error response body
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading error response body: %v", err)
			// Restore the response body and return original error
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			return resp, nil
		}

		// Check if the error is about a missing model
		var errorResp map[string]interface{}
		if err := json.Unmarshal(respBody, &errorResp); err == nil {
			if errMsg, ok := errorResp["error"].(string); ok &&
				(strings.Contains(errMsg, "model not found") ||
					strings.Contains(errMsg, "no model") ||
					strings.Contains(errMsg, "model") && strings.Contains(errMsg, "not found")) {

				// Extract model name from original request
				var bodyJSON map[string]interface{}
				modelName := ""
				if err := json.Unmarshal(originalBody, &bodyJSON); err == nil {
					// Check model field first, then name field
					if model, ok := bodyJSON["model"].(string); ok {
						modelName = model
					} else if name, ok := bodyJSON["name"].(string); ok {
						modelName = name
					}
				}

				if modelName != "" {
					log.Printf("Model '%s' not found. Attempting to pull the model first...", modelName)

					// Construct the pull request URL
					pullURL := fmt.Sprintf("%s://%s/api/pull", req.URL.Scheme, req.URL.Host)

					// Create pull request body
					pullBody := map[string]interface{}{
						"name": modelName,
					}
					pullJSON, _ := json.Marshal(pullBody)

					// Create the pull request
					pullReq, err := http.NewRequest("POST", pullURL, bytes.NewReader(pullJSON))
					if err != nil {
						log.Printf("Error creating pull request: %v", err)
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}

					// Copy relevant headers
					pullReq.Header.Set("Content-Type", "application/json")
					if auth := req.Header.Get("Authorization"); auth != "" {
						pullReq.Header.Set("Authorization", auth)
					}

					log.Printf("Sending pull request for model: %s", modelName)

					// Execute pull request
					pullResp, err := client.Do(pullReq)
					if err != nil {
						log.Printf("Error during model pull: %v", err)
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}
					defer pullResp.Body.Close()

					// Check if pull was successful
					if pullResp.StatusCode != http.StatusOK {
						pullRespBody, _ := io.ReadAll(pullResp.Body)
						log.Printf("Pull request failed with status %d: %s", pullResp.StatusCode, string(pullRespBody))
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}

					// Read pull response body to ensure it completes
					_, err = io.Copy(io.Discard, pullResp.Body)
					if err != nil {
						log.Printf("Error reading pull response: %v", err)
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}

					log.Printf("Successfully pulled model: %s. Retrying original request...", modelName)

					// Recreate the original request
					retryReq, err := http.NewRequest(req.Method, req.URL.String(), bytes.NewReader(originalBody))
					if err != nil {
						log.Printf("Error recreating original request: %v", err)
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}

					// Copy all headers from the original request
					for key, vals := range req.Header {
						retryReq.Header[key] = vals
					}

					// Execute the retry request
					retryResp, err := client.Do(retryReq)
					if err != nil {
						log.Printf("Error during retry request: %v", err)
						// Return original error response
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						return resp, nil
					}

					// Return the retry response
					log.Printf("Retry request completed with status: %d", retryResp.StatusCode)
					return retryResp, nil
				}
			}
		}

		// If we couldn't handle the error, restore the body and return the original response
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	} else if resp.StatusCode != http.StatusOK {
		// Debug non-200 responses
		// Read the response body for logging
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading error response body: %v", err)
		} else {
			// Log the error response body
			log.Printf("ERROR RESPONSE (%d): %s", resp.StatusCode, string(respBody))
		}
		// Restore the response body so it can be read again by downstream code
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}

	return resp, nil
}

// addDefaultTagToModel adds ":latest" to model names that don't have a tag
func addDefaultTagToModel(modelName string) string {
	if modelName == "" {
		return modelName
	}

	if !strings.Contains(modelName, ":") {
		return modelName + ":latest"
	}

	return modelName
}

// processModelBody parses and mutates an Ollama JSON request body.
// It ensures model/name fields have a default :latest tag and for /api/show keeps both
// name and model so upstream implementations that expect either will work.
// Returns potentially modified bytes and a boolean indicating modification.
func processModelBody(path string, body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var bodyJSON map[string]interface{}
	if err := json.Unmarshal(body, &bodyJSON); err != nil {
		return body, false
	}
	modified := false

	// Helper to update a single field
	updateField := func(field string) {
		if v, ok := bodyJSON[field].(string); ok && v != "" {
			updated := addDefaultTagToModel(v)
			if updated != v {
				bodyJSON[field] = updated
				if Verbose {
					log.Printf("Updated model in body (%s field): %s -> %s", field, v, updated)
				}
				modified = true
			}
		}
	}

	updateField("name")
	updateField("model")

	// /api/show specific handling: ensure both name and model exist and are identical
	if strings.HasSuffix(path, "/api/show") {
		nameVal, hasName := bodyJSON["name"].(string)
		modelVal, hasModel := bodyJSON["model"].(string)
		switch {
		case hasName && !hasModel:
			bodyJSON["model"] = nameVal
			modified = true
			if Verbose {
				log.Printf("Added missing 'model' field for /api/show using name=%s", nameVal)
			}
		case hasModel && !hasName:
			bodyJSON["name"] = modelVal
			modified = true
			if Verbose {
				log.Printf("Added missing 'name' field for /api/show using model=%s", modelVal)
			}
		case hasName && hasModel && nameVal != modelVal:
			// Prefer name, overwrite model to match
			bodyJSON["model"] = nameVal
			modified = true
			if Verbose {
				log.Printf("Normalized differing name/model for /api/show: name=%s model=%s", nameVal, modelVal)
			}
		}
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

// PingEndpoint checks if the Ollama endpoint is accessible by making a request to /tags
// Returns nil if successful, error otherwise
func PingEndpoint(targetURL string, apiKey string) error {
	// Add http:// prefix if no protocol is specified
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "http://" + targetURL
	}

	// Parse the target URL
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid target URL: %v", err)
	}

	// Create a client with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Build the tags endpoint URL
	tagsURL := fmt.Sprintf("%s://%s/api/tags", target.Scheme, target.Host)

	// Create the request
	req, err := http.NewRequest("GET", tagsURL, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	// Add the Authorization header if API key is set
	if apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}

	// Make the request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error connecting to Ollama endpoint: %w", err)
	}
	defer resp.Body.Close()

	// Check the status code
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error response from Ollama endpoint (status %d): %s",
			resp.StatusCode, string(bodyBytes))
	}

	return nil
}
