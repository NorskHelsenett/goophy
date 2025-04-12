package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jonasbg/goophy/internal/proxy"
	"github.com/jonasbg/goophy/internal/updater"
)

// Set by GoReleaser via ldflags: -X main.version=...
var version = "dev"

func main() {
	// Define a version flag
	showVersion := flag.Bool("version", false, "Print the version and exit")
	flag.BoolVar(showVersion, "v", false, "Print the version and exit (shorthand)")
	flag.Parse()

	if *showVersion {
		fmt.Println("goophy version:", version)
		os.Exit(0)
	}

	// Initialize auto-updater with default options
	autoUpdater := updater.New(updater.DefaultOptions(version))
	autoUpdater.Start()
	defer autoUpdater.Stop()

	// Get environment variables
	port := getEnv("PORT", "22434")
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
