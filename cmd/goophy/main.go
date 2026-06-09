package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	goophy "github.com/NorskHelsenett/goophy"
	"github.com/NorskHelsenett/goophy/internal/env"
	"github.com/NorskHelsenett/goophy/internal/proxy"
	"github.com/NorskHelsenett/goophy/internal/updater"
)

// Set by GoReleaser via ldflags: -X main.version=...
var version = "dev"

// displayVersion returns a human-facing version string. Tagged release builds
// set `version` via ldflags; otherwise we fall back to the version documented
// in the embedded CHANGELOG.md so local and container builds still report a
// meaningful version. The auto-update gate keeps using the raw `version`.
func displayVersion() string {
	if version != "dev" {
		return version
	}
	if v := goophy.ChangelogVersion(); v != "" {
		return v + "-dev"
	}
	return version
}

func printGlobalHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", displayVersion())
	fmt.Fprintf(os.Stderr, "Usage: goophy [options] [command]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            Show this help message and exit\n")
	fmt.Fprintf(os.Stderr, "  -v, --version         Print the version and exit\n")
	fmt.Fprintf(os.Stderr, "  --env-file string     Path to an env file to load configuration from\n\n")

	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  serve          Start the Ollama proxy server\n")
	fmt.Fprintf(os.Stderr, "  update         Check for and apply available updates\n\n")

	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  HOST             Interface to bind to (default: 127.0.0.1; use 0.0.0.0 for non-localhost)\n")
	fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT  Target Ollama API endpoint (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  Environment variables can also be set in a .env file in the current directory\n")
	fmt.Fprintf(os.Stderr, "  or specified using the --env-file flag.\n\n")

	fmt.Fprintf(os.Stderr, "Description:\n")
	fmt.Fprintf(os.Stderr, "  Goophy is a reverse proxy for Ollama API that provides additional features:\n")
	fmt.Fprintf(os.Stderr, "   - Automatic updates\n")
	fmt.Fprintf(os.Stderr, "   - API key authentication\n")
	fmt.Fprintf(os.Stderr, "   - CORS support for browser applications\n\n")

	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT=http://my-ollama-server:11434 API_KEY=secret123 goophy serve\n\n")
}

func printServeHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", displayVersion())
	fmt.Fprintf(os.Stderr, "Usage: goophy serve [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            Show this help message and exit\n")
	fmt.Fprintf(os.Stderr, "  -V, --verbose         Enable verbose logging\n")
	fmt.Fprintf(os.Stderr, "  --host string         Interface to bind to (default: 127.0.0.1)\n")
	fmt.Fprintf(os.Stderr, "  --listen-all          Listen on all interfaces (shorthand for --host 0.0.0.0)\n")
	fmt.Fprintf(os.Stderr, "  --env-file string     Path to an env file to load configuration from\n\n")

	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  HOST             Interface to bind to (default: 127.0.0.1; use 0.0.0.0 for non-localhost)\n")
	fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT  Target Ollama API endpoint (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  Environment variables can also be set in a .env file in the current directory\n")
	fmt.Fprintf(os.Stderr, "  or specified using the --env-file flag.\n\n")

	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  OLLAMA_ENDPOINT=http://my-ollama-server:11434 API_KEY=secret123 goophy serve\n\n")
}

func printUpdateHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", displayVersion())
	fmt.Fprintf(os.Stderr, "Usage: goophy update [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            Show this help message and exit\n")
	fmt.Fprintf(os.Stderr, "  --env-file string     Path to an env file to load configuration from\n\n")

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
	envFile := globalFlags.String("env-file", "", "Path to an env file to load configuration from")

	// Check for no args or help flag at top level
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		printGlobalHelp()
		os.Exit(0)
	}

	// Handle version flag at top level
	if os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Println("goophy version:", displayVersion())
		os.Exit(0)
	}

	// Check for env-file flag at top level and load environment
	// Also find the command index (skipping --env-file and its value)
	cmdIndex := 1
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--env-file" && i+1 < len(os.Args) {
			*envFile = os.Args[i+1]
			i++ // skip the value
			cmdIndex = i + 1
		} else if os.Args[i] != "" && os.Args[i][0] != '-' {
			cmdIndex = i
			break
		}
	}

	loadEnvFile(envFile, true)

	// Parse command
	if cmdIndex >= len(os.Args) {
		printGlobalHelp()
		os.Exit(0)
	}

	cmd := os.Args[cmdIndex]
	cmdArgs := os.Args[cmdIndex+1:]

	switch cmd {
	case "serve":
		serveCommand(cmdArgs)
	case "update":
		updateCommand(cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printGlobalHelp()
		os.Exit(1)
	}
}

func serveCommand(args []string) {
	// Setup flags for serve command
	serveFlags := flag.NewFlagSet("serve", flag.ExitOnError)
	showHelp := serveFlags.Bool("help", false, "Show help message for serve command")
	serveFlags.BoolVar(showHelp, "h", false, "Show help message for serve command (shorthand)")
	verboseFlag := serveFlags.Bool("verbose", false, "Enable verbose logging")
	serveFlags.BoolVar(verboseFlag, "V", false, "Enable verbose logging (shorthand)")
	// Bind address. Empty default means "fall back to the HOST env / 127.0.0.1";
	// an explicit flag value overrides the env.
	hostFlag := serveFlags.String("host", "", "Interface to bind to (default 127.0.0.1; use 0.0.0.0 to allow non-localhost requests)")
	listenAllFlag := serveFlags.Bool("listen-all", false, "Listen on all interfaces (shorthand for --host 0.0.0.0)")

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

	// Set verbose mode in proxy package
	if *verboseFlag {
		proxy.Verbose = true
		log.Printf("Verbose logging enabled")
	}

	// Get environment variables
	port := env.GetEnv("PORT", "22434")
	targetURL := env.GetEnv("OLLAMA_ENDPOINT", "http://localhost:11434")
	apiKey := env.GetEnv("API_KEY", "")

	// Resolve the bind host: --host flag wins, then --listen-all, then the HOST
	// env, defaulting to localhost-only.
	host := env.GetEnv("HOST", "127.0.0.1")
	if *listenAllFlag {
		host = "0.0.0.0"
	}
	if *hostFlag != "" {
		host = *hostFlag
	}

	// Display configured environment variables
	fmt.Println("Starting with configuration:")
	fmt.Printf("  VERSION:          %s\n", displayVersion())
	fmt.Printf("  HOST:             %s\n", host)
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

	// Cancel pending work (health check, in-flight requests) on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialize auto-updater with default options
	autoUpdater := updater.New(updater.DefaultOptions(version))
	autoUpdater.Start()
	defer autoUpdater.Stop()

	if err := proxy.PingEndpoint(ctx, targetURL, apiKey); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Ollama endpoint: %v\n", err)
		os.Exit(1)
	}

	// Create the proxy
	ollamaProxy, err := proxy.NewOllamaProxy(host, port, targetURL, apiKey)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}

	// Start the server (blocks until the context is cancelled or the server fails).
	if err := ollamaProxy.Start(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
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

func loadEnvFile(envFile *string, reportError bool) {
	if err := env.LoadEnvOptional(*envFile); err != nil && reportError {
		fmt.Fprintf(os.Stderr, "Error loading env file: %v\n", err)
		os.Exit(1)
	}
}
