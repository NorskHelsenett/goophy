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
	// Create a custom FlagSet with custom usage function
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage: goophy [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -h, --help     Show this help message and exit\n")
		fmt.Fprintf(os.Stderr, "  -v, --version  Print the version and exit\n\n")
		
		fmt.Fprintf(os.Stderr, "Environment Variables:\n")
		fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
		fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT  Target Ollama API endpoint (default: http://localhost:11434)\n")
		fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n\n")
		
		fmt.Fprintf(os.Stderr, "Description:\n")
		fmt.Fprintf(os.Stderr, "  Goophy is a reverse proxy for Ollama API that provides additional features:\n")
		fmt.Fprintf(os.Stderr, "   - Automatic updates\n")
		fmt.Fprintf(os.Stderr, "   - API key authentication\n")
		fmt.Fprintf(os.Stderr, "   - Automatic model pulling\n")
		fmt.Fprintf(os.Stderr, "   - Response format transformation\n")
		fmt.Fprintf(os.Stderr, "   - CORS support for browser applications\n\n")
		
		fmt.Fprintf(os.Stderr, "Example usage:\n")
		fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT=http://my-ollama-server:11434 API_KEY=secret123 goophy\n\n")
	}

	// Define flags
	showVersion := flag.Bool("version", false, "Print the version and exit")
	flag.BoolVar(showVersion, "v", false, "Print the version and exit (shorthand)")
	showHelp := flag.Bool("help", false, "Show help message")
	flag.BoolVar(showHelp, "h", false, "Show help message (shorthand)")
	flag.Parse()

	// Handle help flag
	if *showHelp {
		flag.Usage()
		os.Exit(0)
	}

	// Handle version flag
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

	// First ping the endpoint to ensure it's accessible
	fmt.Printf("Checking Ollama endpoint at %s...\n", targetURL)
	if err := proxy.PingEndpoint(targetURL, apiKey); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Ollama endpoint: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Ollama endpoint is accessible.")

	// Create the proxy
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
