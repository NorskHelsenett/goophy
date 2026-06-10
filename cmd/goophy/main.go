package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	fmt.Fprintf(os.Stderr, "  API_ENDPOINT     Target API endpoint to forward requests to (default: http://localhost:11434)\n")
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
	fmt.Fprintf(os.Stderr, "  API_ENDPOINT=https://api.example.com/v1 API_KEY=secret123 goophy serve\n\n")
}

func printServeHelp() {
	fmt.Fprintf(os.Stderr, "Goophy - Ollama API Proxy, version %s\n\n", displayVersion())
	fmt.Fprintf(os.Stderr, "Usage: goophy serve [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help                Show this help message and exit\n")
	fmt.Fprintf(os.Stderr, "  -V, --verbose             Enable verbose logging\n")
	fmt.Fprintf(os.Stderr, "  --host string             Interface to bind to (default: 127.0.0.1)\n")
	fmt.Fprintf(os.Stderr, "  --port string             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  --api-endpoint string     Target API endpoint to forward requests to (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  --api-key string          API key for authentication (default: none)\n")
	fmt.Fprintf(os.Stderr, "  --listen-all              Listen on all interfaces (shorthand for --host 0.0.0.0)\n")
	fmt.Fprintf(os.Stderr, "  --env-file string         Path to an env file to load configuration from\n\n")

	fmt.Fprintf(os.Stderr, "Environment Variables:\n")
	fmt.Fprintf(os.Stderr, "  HOST             Interface to bind to (default: 127.0.0.1; use 0.0.0.0 for non-localhost)\n")
	fmt.Fprintf(os.Stderr, "  PORT             Port number to listen on (default: 22434)\n")
	fmt.Fprintf(os.Stderr, "  API_ENDPOINT     Target API endpoint to forward requests to (default: http://localhost:11434)\n")
	fmt.Fprintf(os.Stderr, "  API_KEY          Optional API key for authentication (default: none)\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  Environment variables can also be set in a .env file in the current directory\n")
	fmt.Fprintf(os.Stderr, "  or specified using the --env-file flag.\n\n")

	fmt.Fprintf(os.Stderr, "Example usage:\n")
	fmt.Fprintf(os.Stderr, "  API_ENDPOINT=https://api.example.com/v1 API_KEY=secret123 goophy serve\n\n")
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

// knownCommands lists the subcommands goophy understands. It is used to locate
// the command token regardless of where flags are placed around it, so global
// and command flags may appear either before or after the command.
var knownCommands = map[string]bool{
	"serve":  true,
	"update": true,
}

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		printGlobalHelp()
		os.Exit(0)
	}

	// --env-file may appear before or after the command (e.g. both
	// "goophy --env-file f serve" and "goophy serve --env-file f"). Extract and
	// load it first so environment-derived configuration is available to every
	// command, and so the per-command flag sets never see it.
	envFile, args := extractEnvFile(args)
	loadEnvFile(&envFile, true)

	// Find the command anywhere in the remaining args; everything else is
	// forwarded to the command's flag set, preserving order.
	cmd, cmdArgs := splitCommand(args)

	switch cmd {
	case "serve":
		serveCommand(cmdArgs)
	case "update":
		updateCommand(cmdArgs)
	default:
		handleNoCommand(args)
	}
}

// splitCommand returns the first token naming a known command together with all
// remaining arguments (order preserved). This allows flags to be placed before
// or after the command on the command line.
func splitCommand(args []string) (cmd string, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if cmd == "" && knownCommands[a] {
			cmd = a
			continue
		}
		rest = append(rest, a)
	}
	return cmd, rest
}

// extractEnvFile pulls an --env-file value from anywhere in args, supporting
// both "--env-file path" and "--env-file=path" (and single-dash) forms. The
// flag and its value are removed from the returned args so downstream command
// flag parsing never sees it. The last occurrence wins.
func extractEnvFile(args []string) (envFile string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--env-file" || a == "-env-file":
			if i+1 < len(args) {
				envFile = args[i+1]
				i++ // skip the value
			}
		case strings.HasPrefix(a, "--env-file="):
			envFile = strings.TrimPrefix(a, "--env-file=")
		case strings.HasPrefix(a, "-env-file="):
			envFile = strings.TrimPrefix(a, "-env-file=")
		default:
			rest = append(rest, a)
		}
	}
	return envFile, rest
}

// handleNoCommand deals with invocations that contain no recognized command:
// top-level help/version flags, an unknown command, or nothing at all.
func handleNoCommand(args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printGlobalHelp()
			os.Exit(0)
		}
		if a == "-v" || a == "--version" {
			fmt.Println("goophy version:", displayVersion())
			os.Exit(0)
		}
	}
	// Report the first non-flag token as an unknown command, if any.
	for _, a := range args {
		if a != "" && a[0] != '-' {
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", a)
			printGlobalHelp()
			os.Exit(1)
		}
	}
	printGlobalHelp()
	os.Exit(0)
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
	listenAllFlag := serveFlags.Bool("listen-all", false, "Listen on all interfaces (shorthand for --host 0.0.0.0)")
	// Every option below supports both a --flag and an environment variable.
	// An empty flag default means "fall back to the env var / built-in default";
	// an explicit flag value overrides the env (see resolve).
	hostFlag := serveFlags.String("host", "", "Interface to bind to (default 127.0.0.1; use 0.0.0.0 to allow non-localhost requests)")
	portFlag := serveFlags.String("port", "", "Port number to listen on (default 22434)")
	endpointFlag := serveFlags.String("api-endpoint", "", "Target API endpoint to forward requests to (default http://localhost:11434)")
	apiKeyFlag := serveFlags.String("api-key", "", "API key for authentication (default none)")
	// Deprecated alias for --api-endpoint, kept so existing setups keep working.
	ollamaEndpointFlag := serveFlags.String("ollama-endpoint", "", "Deprecated: use --api-endpoint")

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

	// Resolve configuration: an explicit --flag wins over the env var, which
	// wins over the built-in default.
	port := resolve(*portFlag, "PORT", "22434")
	apiKey := resolve(*apiKeyFlag, "API_KEY", "")

	// Upstream endpoint: prefer the API_ENDPOINT / --api-endpoint option, but
	// fall back to the deprecated OLLAMA_ENDPOINT / --ollama-endpoint with a
	// warning so existing configurations keep working.
	targetURL := resolveAlias(
		alias{*endpointFlag, "API_ENDPOINT", "--api-endpoint"},
		alias{*ollamaEndpointFlag, "OLLAMA_ENDPOINT", "--ollama-endpoint"},
		"http://localhost:11434",
	)

	// Bind host follows the same rule, with --listen-all as a 0.0.0.0 shorthand
	// that the explicit --host flag still overrides.
	host := resolve(*hostFlag, "HOST", "127.0.0.1")
	if *listenAllFlag && *hostFlag == "" {
		host = "0.0.0.0"
	}

	// Display configured environment variables
	fmt.Println("Starting with configuration:")
	fmt.Printf("  VERSION:          %s\n", displayVersion())
	fmt.Printf("  HOST:             %s\n", host)
	fmt.Printf("  PORT:             %s\n", port)
	fmt.Printf("  API_ENDPOINT:     %s\n", targetURL)

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

// resolve returns the flag value when set (non-empty), otherwise the value of
// the named environment variable, otherwise the default. This gives every
// option both a --flag and an env-var syntax, with the flag taking precedence.
func resolve(flagVal, envKey, def string) string {
	if flagVal != "" {
		return flagVal
	}
	return env.GetEnv(envKey, def)
}

// alias describes one source for an option: a flag value, the env var backing
// it, and the human-facing flag name used in deprecation warnings.
type alias struct {
	flagVal string
	envKey  string
	name    string
}

// resolveAlias resolves an option that has a primary name and one or more
// deprecated aliases. Sources are tried in order — for each, the flag value
// wins over its env var — and the first non-empty value is used. When the
// winning source is anything other than the first (primary) alias, a
// deprecation warning naming the primary is printed. Falls back to def.
func resolveAlias(primary alias, deprecated alias, def string) string {
	if v := resolve(primary.flagVal, primary.envKey, ""); v != "" {
		return v
	}
	if v := resolve(deprecated.flagVal, deprecated.envKey, ""); v != "" {
		log.Printf("warning: %s/%s is deprecated; use %s/%s instead",
			deprecated.name, deprecated.envKey, primary.name, primary.envKey)
		return v
	}
	return def
}

func loadEnvFile(envFile *string, reportError bool) {
	if err := env.LoadEnvOptional(*envFile); err != nil && reportError {
		fmt.Fprintf(os.Stderr, "Error loading env file: %v\n", err)
		os.Exit(1)
	}
}
