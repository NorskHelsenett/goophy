// Package updater provides auto-update functionality for the application
package updater

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-github/v50/github"
	"github.com/inconshreveable/go-update"
	"github.com/jonasbg/goophy/internal/archive"
)

var (
	githubOwner = "jonasbg" // GitHub username/organization
	githubRepo  = "goophy"  // GitHub repository name
)

// Options defines options for the auto-updater
type Options struct {
	CurrentVersion     string
	CheckInterval      time.Duration
	AutoDownload       bool
	UpdateURL          string
	GithubTokenEnvVar  string
	DisableAutoUpdates bool
}

// DefaultOptions returns the default update options
func DefaultOptions(currentVersion string) Options {
	// Parse environment variables
	disableAutoUpdate := strings.ToLower(os.Getenv("DISABLE_AUTO_UPDATE")) == "true"

	// Parse custom check interval if provided
	checkInterval := 24 * time.Hour // Default: 24 hours
	if intervalStr := os.Getenv("UPDATE_CHECK_INTERVAL"); intervalStr != "" {
		if interval, err := time.ParseDuration(intervalStr); err == nil {
			checkInterval = interval
		} else {
			log.Printf("Warning: Invalid UPDATE_CHECK_INTERVAL format (%s), using default 24h", intervalStr)
		}
	}

	return Options{
		CurrentVersion:     currentVersion,
		CheckInterval:      checkInterval,
		AutoDownload:       true,
		GithubTokenEnvVar:  "GITHUB_TOKEN",
		DisableAutoUpdates: disableAutoUpdate,
	}
}

// AutoUpdater manages the application updates
type AutoUpdater struct {
	options Options
	client  *github.Client
	ticker  *time.Ticker
	stopCh  chan struct{}
}

// New creates a new auto updater
func New(options Options) *AutoUpdater {
	// Create a GitHub client
	client := github.NewClient(nil)

	// Use token if available
	token := os.Getenv(options.GithubTokenEnvVar)
	if token != "" {
		client = github.NewClient(oauth2Transport(token))
	}

	return &AutoUpdater{
		options: options,
		client:  client,
		stopCh:  make(chan struct{}),
	}
}

func oauth2Transport(token string) *http.Client {
	return &http.Client{
		Transport: &tokenTransport{
			token: token,
		},
	}
}

type tokenTransport struct {
	token string
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("token %s", t.token))
	return http.DefaultTransport.RoundTrip(req)
}

// Start begins the auto-update process
func (u *AutoUpdater) Start() {
	if u.options.DisableAutoUpdates {
		log.Println("Auto-updates are disabled")
		return
	}

	u.ticker = time.NewTicker(u.options.CheckInterval)

	// Initial check immediately
	go u.checkForUpdate()

	// Regular checks based on interval
	go func() {
		for {
			select {
			case <-u.ticker.C:
				u.checkForUpdate()
			case <-u.stopCh:
				u.ticker.Stop()
				return
			}
		}
	}()

	log.Printf("Auto-updater started - current version: %s", u.options.CurrentVersion)
}

// Stop stops the auto-updater
func (u *AutoUpdater) Stop() {
	close(u.stopCh)
}

// checkForUpdate checks for new updates and applies them if available
func (u *AutoUpdater) checkForUpdate() {
	log.Println("Checking for updates...")

	latestRelease, err := u.getLatestRelease()
	if err != nil {
		log.Printf("Error checking for updates: %s", err)
		return
	}

	latestVersion := strings.TrimPrefix(latestRelease.GetTagName(), "v")
	if latestVersion == u.options.CurrentVersion {
		log.Printf("Already running the latest version: %s", latestVersion)
		return
	}

	log.Printf("New version available: %s (current: %s)", latestVersion, u.options.CurrentVersion)

	if !u.options.AutoDownload {
		log.Println("Auto-download disabled, skipping update")
		return
	}

	asset, err := u.findAssetForCurrentPlatform(latestRelease)
	if err != nil {
		log.Printf("Error finding asset for current platform: %s", err)
		return
	}

	err = u.downloadAndApplyUpdate(asset.GetBrowserDownloadURL(), asset.GetName())
	if err != nil {
		log.Printf("Error applying update: %s", err)
		return
	}

	log.Printf("Successfully updated to version %s", latestVersion)
}

// getLatestRelease gets the latest release from GitHub
func (u *AutoUpdater) getLatestRelease() (*github.RepositoryRelease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	release, _, err := u.client.Repositories.GetLatestRelease(ctx, githubOwner, githubRepo)
	return release, err
}

// findAssetForCurrentPlatform finds the correct binary for the current platform
func (u *AutoUpdater) findAssetForCurrentPlatform(release *github.RepositoryRelease) (*github.ReleaseAsset, error) {
	os := runtime.GOOS
	arch := runtime.GOARCH

	// Map Go's OS and arch names to the ones used in release asset names
	if os == "darwin" {
		os = "macos"
	}

	// The GitHub release assets might have capitalized OS names like "Linux" instead of "linux"
	// We'll handle this by doing a case-insensitive comparison
	for _, asset := range release.Assets {
		name := asset.GetName()
		nameLower := strings.ToLower(name)
		osLower := strings.ToLower(os)
		archLower := strings.ToLower(arch)
		
		// Check if the asset name contains both the OS and architecture
		if strings.Contains(nameLower, osLower) && strings.Contains(nameLower, archLower) {
			return asset, nil
		}
	}

	return nil, fmt.Errorf("no compatible binary found for %s/%s", os, arch)
}

// downloadAndApplyUpdate downloads and applies the update
func (u *AutoUpdater) downloadAndApplyUpdate(url, assetName string) error {
	log.Printf("Downloading update from %s", url)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %s", resp.Status)
	}

	// Read the entire response into a buffer
	var bodyBytes []byte
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Binary name is the same as the executable name
	currentExePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current executable path: %w", err)
	}
	binaryName := filepath.Base(currentExePath)

	// Get binary from archive based on file extension
	var updateData io.Reader
	fileExt := strings.ToLower(filepath.Ext(assetName))

	if strings.HasSuffix(assetName, ".tar.gz") || fileExt == ".gz" {
		// Handle tar.gz archive
		updateData, err = archive.ExtractBinaryFromTarGz(bytes.NewReader(bodyBytes), binaryName)
	} else if fileExt == ".zip" {
		// Handle zip archive
		updateData, err = archive.ExtractBinaryFromZip(bytes.NewReader(bodyBytes), int64(len(bodyBytes)), binaryName)
	} else {
		// Direct binary
		updateData = bytes.NewReader(bodyBytes)
	}

	if err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	// Apply the update
	err = update.Apply(updateData, update.Options{})
	if err != nil {
		// Attempt to roll back
		if rerr := update.RollbackError(err); rerr != nil {
			log.Printf("Failed to rollback from bad update: %v", rerr)
		}
		return err
	}

	return nil
}
