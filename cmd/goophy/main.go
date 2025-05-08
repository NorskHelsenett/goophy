package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/NorskHelsenett/goophy/internal/proxy"
	"github.com/NorskHelsenett/goophy/internal/updater"
)

// Set by GoReleaser via ldflags: -X main.version=...
var version = "dev"

func printGlobalHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage: goophy [options] [command]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help     Show this help message and exit\n")
	fmt.Fprintf(os.Stderr, "  -v, --version  Print the version and exit\n\n")
	
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  serve          Start the Ollama proxy server\n")
	fmt.Fprintf(os.Stderr, "  update         Check for and apply available updates\n\n")
	
	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT  Target Ollama API endpoint (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n\n")
	
	fmt.Fprintf(os.Stderr, "Description:\n")
	fmt.Fprintf(os.Stderr, "  Goophy is a reverse proxy for Ollama API that provides additional features:\n")
	fmt.Fprintf(os.Stderr, "   - Automatic updates\n")
	fmt.Fprintf(os.Stderr, "   - API key authentication\n")
	fmt.Fprintf(os.Stderr, "   - CORS support for browser applications\n\n")
	
	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT=http://my-ollama-server:11434 API_KEY=secret123 goophy serve\n\n")
}

func printServeHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage: goophy serve [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help     Show this help message and exit\n\n")
	
	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT  Target Ollama API endpoint (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n\n")
	
	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT=http://my-ollama-server:11434 API_KEY=secret123 goophy serve\n\n")
}

func printUpdateHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage: goophy update [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help     Show this help message and exit\n\n")

	fmt.Fprintf(os.Stderr, "Description:\n")
	fmt.Fprintf(os.Stderr, "  Checks for and applies available updates for Goophy.\n")
	fmt.Fprintf(os.Stderr, "  If running a development version (dev), updates are not available.\n\n")

	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  goophy update\n\n")
}

func main() {
	// Global flags
	globalFlags := flag.NewFlagSet("global", flag.ExitOnError)
	showVersion := globalFlags.Bool("version", false, "Print the version and exit")
	globalFlags.BoolVar(showVersion, "v", false, "Print the version and exit (shorthand)")
	showHelp := globalFlags.Bool("help", false, "Show help message")
	globalFlags.BoolVar(showHelp, "h", false, "Show help message (shorthand)")

	// Check for no args or help flag at top level
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		printGlobalHelp()
		os.Exit(0)
	}

	// Handle version flag at top level
	if os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Println("goophy version:", version)
		os.Exit(0)
	}

	// Parse command
	switch os.Args[1] {
	case "serve":
		serveCommand(os.Args[2:])
	case "update":
		updateCommand(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printGlobalHelp()
		os.Exit(1)
	}
}

func serveCommand(args []string) {
	// Setup flags for serve command
	serveFlags := flag.NewFlagSet("serve", flag.ExitOnError)
	showHelp := serveFlags.Bool("help", false, "Show help message for serve command")
	serveFlags.BoolVar(showHelp, "h", false, "Show help message for serve command (shorthand)")
	
	// Set custom usage function
	serveFlags.Usage = printServeHelp
	
	// Parse serve command flags
	err := serveFlags.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	// Show help if requested
	if *showHelp {
		printServeHelp()
		os.Exit(0)
	}

	// Get environment variables
	port := getEnv("PORT", "22434")
	targetURL := getEnv("OLLAMA_ENDPOINT", "http://localhost:11434")
	apiKey := getEnv("API_KEY", "")

	// Display configured environment variables
	fmt.Println("Starting with configuration:")
	fmt.Printf("  PORT:             %s\n", port)
	fmt.Printf("  OLLAMA_ENDPOINT:  %s\n", targetURL)
	
	// Mask API key if present
	apiKeyDisplay := "Not configured"
	if apiKey != "" {
		// Show only first and last character if key is long enough
		if len(apiKey) > 5 {
			apiKeyDisplay = fmt.Sprintf("%s***%s", apiKey[:1], apiKey[len(apiKey)-1:])
		} else {
			apiKeyDisplay = "******" // Mask completely if too short
		}
	}
	fmt.Printf("  API_KEY:          %s\n\n", apiKeyDisplay)

	// Initialize auto-updater with default options
	autoUpdater := updater.New(updater.DefaultOptions(version))
	autoUpdater.Start()
	defer autoUpdater.Stop()

	if err := proxy.PingEndpoint(targetURL, apiKey); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Ollama endpoint: %v\n", err)
		os.Exit(1)
	}

	// Create the proxy
	ollamaProxy, err := proxy.NewOllamaProxy(port, targetURL, apiKey)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}

	// Start the server (this will block until the server is stopped)
	log.Fatal(ollamaProxy.Start())
}

func updateCommand(args []string) {
	// Setup flags for update command
	updateFlags := flag.NewFlagSet("update", flag.ExitOnError)
	showHelp := updateFlags.Bool("help", false, "Show help message for update command")
	updateFlags.BoolVar(showHelp, "h", false, "Show help message for update command (shorthand)")

	// Set custom usage function
	updateFlags.Usage = printUpdateHelp

	// Parse update command flags
	err := updateFlags.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	// Show help if requested
	if *showHelp {
		printUpdateHelp()
		os.Exit(0)
	}

	// Don't allow updates for dev version
	if version == "dev" {
		fmt.Println("Running development version, updates not available.")
		fmt.Println("Please build from source or download a released version.")
		os.Exit(0)
	}

	// Create updater
	fmt.Printf("Current version: %s\n", version)
	fmt.Println("Checking for updates...")
	
	autoUpdater := updater.New(updater.DefaultOptions(version))
	
	// Check for updates first
	updateInfo, err := autoUpdater.CheckForUpdates()
	if err != nil {
		fmt.Printf("Error checking for updates: %v\n", err)
		os.Exit(1)
	}
	
	if !updateInfo.IsUpdateAvailable {
		fmt.Printf("Already running the latest version: %s\n", updateInfo.CurrentVersion)
		os.Exit(0)
	}
	
	fmt.Printf("New version available: %s\n", updateInfo.LatestVersion)
	fmt.Println("Downloading and applying update...")
	
	// Apply update
	err = autoUpdater.ApplyUpdate()
	if err != nil {
		fmt.Printf("Error applying update: %v\n", err)
		os.Exit(1)
	}
	
	fmt.Printf("Successfully updated to version %s\n", updateInfo.LatestVersion)
	fmt.Println("Please restart the application to use the new version.")
	os.Exit(0)
}

// Helper function to get environment variables with default values
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
