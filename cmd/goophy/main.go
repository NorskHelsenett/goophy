package main

import (
	"log"
	"os"

	"github.com/jonasbg/goophy/internal/proxy"
	"github.com/jonasbg/goophy/internal/updater"
)

func main() {
	// Initialize auto-updater with default options
	autoUpdater := updater.New(updater.DefaultOptions())
	autoUpdater.Start()
	defer autoUpdater.Stop()

	// Get environment variables
	port := getEnv("PORT", "8080")
	targetURL := getEnv("OLLAMA_ENDPOINT", "http://localhost:11434")
	apiKey := getEnv("API_KEY", "")

	// Create and start the proxy
	ollamaProxy, err := proxy.NewOllamaProxy(port, targetURL, apiKey)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}

	// Start the server (this will block until the server is stopped)
	log.Fatal(ollamaProxy.Start())
}

// Helper function to get environment variables with default values
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
