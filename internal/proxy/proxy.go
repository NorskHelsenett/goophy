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
)

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
					// Parse JSON to find and update model field
					var bodyJSON map[string]interface{}
					if err := json.Unmarshal(bodyBytes, &bodyJSON); err == nil {
						// Track if we modified anything
						modified := false

						// Check for "name" field
						if modelName, ok := bodyJSON["name"].(string); ok {
							updatedModelName := addDefaultTagToModel(modelName)
							if modelName != updatedModelName {
								bodyJSON["name"] = updatedModelName
								log.Printf("Updated model in body (name field): %s -> %s", modelName, updatedModelName)
								modified = true
							}
						}

						// Check for "model" field
						if modelName, ok := bodyJSON["model"].(string); ok {
							updatedModelName := addDefaultTagToModel(modelName)
							if modelName != updatedModelName {
								bodyJSON["model"] = updatedModelName
								log.Printf("Updated model in body (model field): %s -> %s", modelName, updatedModelName)
								modified = true
							}
						}

						// Special handling for /api/show endpoint - Ollama expects "name" instead of "model"
						if strings.HasSuffix(r.URL.Path, "/api/show") {
							// Check if we have a model field but no name field
							if modelName, ok := bodyJSON["model"].(string); ok {
								if _, hasName := bodyJSON["name"]; !hasName {
									bodyJSON["name"] = modelName
									delete(bodyJSON, "model") // Remove the model field to avoid confusion
									log.Printf("Transformed field for /api/show: 'model' -> 'name': %s", modelName)
									modified = true
								}
							}
						}

						// Restore the body - use modified JSON if we made changes, otherwise use original bytes
						if modified {
							modifiedBody, err := json.Marshal(bodyJSON)
							if err == nil {
								r.Body = io.NopCloser(bytes.NewReader(modifiedBody))
								// Update Content-Length header to match the new body size
								r.ContentLength = int64(len(modifiedBody))
								r.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))
							} else {
								// If marshaling fails, fall back to original bytes
								r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
							}
						} else {
							// No modifications, use original bytes
							r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
						}
					} else {
						// If JSON parsing failed, restore with original bytes
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

	// Execute the request using our client
	// Using the clean request prevents RequestURI errors
	resp, err := client.Do(newReq)
	if err != nil {
		return nil, err
	}

	// Log the response status
	log.Printf("%s %s -> %d", req.Method, req.URL.Path, resp.StatusCode)

	// Debug non-200 responses
	if resp.StatusCode != http.StatusOK {
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

	// Special handling for the /api/ps endpoint to transform response format
	if strings.HasSuffix(t.originalPath, "/api/ps") && resp.StatusCode == http.StatusOK {
		// Read the original response body
		originalBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		// Transform the response body format
		transformedBody, err := transformPsResponse(originalBody)
		if err != nil {
			// If transformation fails, return original response
			log.Printf("Failed to transform /api/ps response: %v", err)
			resp.Body = io.NopCloser(bytes.NewReader(originalBody))
			return resp, nil
		}

		// Replace the response body with the transformed one
		resp.Body = io.NopCloser(bytes.NewReader(transformedBody))
		resp.ContentLength = int64(len(transformedBody))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
		log.Printf("Transformed /api/ps response format")
	}

	return resp, nil
}

// transformPsResponse transforms the process response from the nested map format to the expected format
func transformPsResponse(data []byte) ([]byte, error) {
	// Parse the original response
	var originalResponse map[string]interface{}
	if err := json.Unmarshal(data, &originalResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal original response: %w", err)
	}

	// Extract all models from all servers and combine them
	var allModels []interface{}
	for _, serverData := range originalResponse {
		if serverDataMap, ok := serverData.(map[string]interface{}); ok {
			if models, ok := serverDataMap["models"].([]interface{}); ok {
				allModels = append(allModels, models...)
			}
		}
	}

	// Create the expected response format
	expectedResponse := map[string]interface{}{
		"models": allModels,
	}

	// Marshal back to JSON
	transformedData, err := json.Marshal(expectedResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed response: %w", err)
	}

	return transformedData, nil
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
