package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

func main() {
	// Initialize auto-updater with default options
	updater := NewAutoUpdater(DefaultUpdateOptions())
	updater.Start()
	defer updater.Stop()

	// Get environment variables
	port := getEnv("PORT", "8080")
	targetURL := getEnv("OLLAMA_ENDPOINT", "http://localhost:11434")
	apiKey := getEnv("API_KEY", "")

	// Add http:// prefix if no protocol is specified
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "http://" + targetURL
		log.Printf("No protocol specified in OLLAMA_ENDPOINT, using: %s", targetURL)
	}

	// Log configuration
	log.Printf("Starting Ollama proxy server on port %s", port)
	log.Printf("Forwarding requests to: %s", targetURL)
	if apiKey != "" {
		log.Print("API Key authentication enabled")
	}

	// Parse the target URL
	target, err := url.Parse(targetURL)
	if err != nil {
		log.Fatalf("Invalid target URL: %v", err)
	}

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Customize the director function to add our authorization header and fix path issues
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Store original path before the original director modifies it
		originalPath := req.URL.Path

		// Call the original director
		originalDirector(req)

		// Set the Host header to the target host
		req.Host = target.Host

		// Add the Authorization header if API key is set
		if apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		}

		// Fix path handling issues
		// First, normalize paths by removing trailing slashes
		targetPath := strings.TrimSuffix(target.Path, "/")

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
			target.Scheme,
			target.Host,
			req.URL.Path)
	}

	// Create a custom transport that preserves the original request and modifies specific responses
	proxy.Transport = &customTransport{
		originalPath: "",
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

	// Create and start the server
	server := &http.Server{
		Addr:    ":" + port,
		Handler: http.HandlerFunc(handler),
	}

	log.Printf("Ollama proxy server started at http://localhost:%s", port)
	log.Fatal(server.ListenAndServe())
}

// Custom transport to preserve the original request and modify specific responses
type customTransport struct {
	originalPath string
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Use the default transport to perform the request
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Log the response status
	log.Printf("%s %s -> %d", req.Method, req.URL.Path, resp.StatusCode)

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

// Helper function to get environment variables with default values
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
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
