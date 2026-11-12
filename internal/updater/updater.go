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

	"github.com/NorskHelsenett/goophy/internal/archive"
	"github.com/google/go-github/v92/github"
	"github.com/inconshreveable/go-update"
)

var (
	githubOwner = "NorskHelsenett" // GitHub username/organization
	githubRepo  = "goophy"         // GitHub repository name
)

// Options defines options for the auto-updater
type Options struct {
	CurrentVersion     string
	CheckInterval      time.Duration
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
		GithubTokenEnvVar:  "GITHUB_TOKEN",
		DisableAutoUpdates: disableAutoUpdate,
	}
}

// UpdateInfo contains information about an available update
type UpdateInfo struct {
	CurrentVersion    string
	LatestVersion     string
	ReleaseURL        string
	IsUpdateAvailable bool
	AssetURL          string
	AssetName         string
}

// AutoUpdater manages the application updates
type AutoUpdater struct {
	options        Options
	client         *github.Client
	ticker         *time.Ticker
	stopCh         chan struct{}
	lastUpdateInfo *UpdateInfo
}

// New creates a new auto updater
func New(options Options) *AutoUpdater {
	// Create a GitHub client, authenticated if a token is available
	var clientOpts []github.ClientOptionsFunc
	if token := os.Getenv(options.GithubTokenEnvVar); token != "" {
		clientOpts = append(clientOpts, github.WithAuthToken(token))
	}
	client, err := github.NewClient(clientOpts...)
	if err != nil {
		log.Printf("Failed to configure GitHub client, using unauthenticated access: %v", err)
		client, _ = github.NewClient()
	}

	return &AutoUpdater{
		options: options,
		client:  client,
		stopCh:  make(chan struct{}),
	}
}

// Start begins the auto-update check process. Checks are run serially on a
// single goroutine so that slow GitHub API responses cannot cause goroutine
// accumulation when ticks fire faster than checks complete.
func (u *AutoUpdater) Start() {
	if u.options.DisableAutoUpdates {
		log.Println("Update checks are disabled")
		return
	}

	// Don't check for updates if running development version
	if u.options.CurrentVersion == "dev" {
		log.Println("Running development version, update checks disabled")
		return
	}

	u.ticker = time.NewTicker(u.options.CheckInterval)

	go func() {
		// Initial check immediately
		u.checkForUpdate()

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

	log.Printf("Update checker started - current version: %s", u.options.CurrentVersion)
}

// Stop stops the auto-updater
func (u *AutoUpdater) Stop() {
	close(u.stopCh)
}

// checkForUpdate checks for new updates and notifies if available
func (u *AutoUpdater) checkForUpdate() {
	log.Println("Checking for updates...")

	// Don't check for updates if running development version
	if u.options.CurrentVersion == "dev" {
		log.Println("Running development version, skipping update check")
		return
	}

	updateInfo, err := u.CheckForUpdates()
	if err != nil {
		log.Printf("Error checking for updates: %s", err)
		return
	}

	u.lastUpdateInfo = updateInfo

	if !updateInfo.IsUpdateAvailable {
		log.Printf("Already running the latest version: %s", updateInfo.CurrentVersion)
		return
	}

	log.Printf("New version available: %s (current: %s)", updateInfo.LatestVersion, updateInfo.CurrentVersion)
	log.Printf("Run 'goophy update' to upgrade to the latest version")
}

// CheckForUpdates checks if a new update is available
func (u *AutoUpdater) CheckForUpdates() (*UpdateInfo, error) {
	// Skip update check for dev version
	if u.options.CurrentVersion == "dev" {
		return &UpdateInfo{
			CurrentVersion:    u.options.CurrentVersion,
			IsUpdateAvailable: false,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	release, err := u.newestRelease(ctx)
	if err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.GetTagName(), "v")
	// Only ever move forward: a preview build must not be "updated" back to
	// the older stable release. An unparsable version falls back to the old
	// rule (any different tag is an update).
	isUpdateAvailable := latestVersion != u.options.CurrentVersion
	if cmp, ok := compareVersions(latestVersion, u.options.CurrentVersion); ok {
		isUpdateAvailable = cmp > 0
	}

	updateInfo := &UpdateInfo{
		CurrentVersion:    u.options.CurrentVersion,
		LatestVersion:     latestVersion,
		ReleaseURL:        release.GetHTMLURL(),
		IsUpdateAvailable: isUpdateAvailable,
	}

	if isUpdateAvailable {
		asset, err := u.findAssetForCurrentPlatform(release)
		if err != nil {
			return updateInfo, err
		}

		updateInfo.AssetURL = asset.GetBrowserDownloadURL()
		updateInfo.AssetName = asset.GetName()

		log.Printf("Found matching asset: %s for %s/%s",
			asset.GetName(), runtime.GOOS, runtime.GOARCH)
	}

	return updateInfo, nil
}

// ApplyUpdate downloads and applies an update
func (u *AutoUpdater) ApplyUpdate() error {
	// Don't update if running dev version
	if u.options.CurrentVersion == "dev" {
		return fmt.Errorf("cannot update development version")
	}

	// Check for updates if we haven't already
	var updateInfo *UpdateInfo
	var err error

	if u.lastUpdateInfo != nil && u.lastUpdateInfo.IsUpdateAvailable {
		updateInfo = u.lastUpdateInfo
	} else {
		updateInfo, err = u.CheckForUpdates()
		if err != nil {
			return fmt.Errorf("failed to check for updates: %w", err)
		}
	}

	if !updateInfo.IsUpdateAvailable {
		return fmt.Errorf("already running the latest version: %s", updateInfo.CurrentVersion)
	}

	if updateInfo.AssetURL == "" || updateInfo.AssetName == "" {
		return fmt.Errorf("no compatible update found for this platform")
	}

	return u.downloadAndApplyUpdate(updateInfo.AssetURL, updateInfo.AssetName)
}

// findAssetForCurrentPlatform finds the release archive for the running platform.
func (u *AutoUpdater) findAssetForCurrentPlatform(release *github.RepositoryRelease) (*github.ReleaseAsset, error) {
	names := make([]string, len(release.Assets))
	for i, asset := range release.Assets {
		names[i] = asset.GetName()
	}
	if i := matchAsset(names, runtime.GOOS, runtime.GOARCH); i >= 0 {
		return release.Assets[i], nil
	}
	return nil, fmt.Errorf("no compatible binary found for %s/%s", runtime.GOOS, runtime.GOARCH)
}

// osAliases and archAliases map Go's GOOS/GOARCH to the spellings that may
// appear in release asset names. GoReleaser's uname-style template produces
// e.g. "goophy_Darwin_x86_64.tar.gz"; legacy aliases such as
// "goophy_legacy_macos_amd64.tar.gz" are also published so that binaries from v0.2.2
// and earlier, which only look for "macos"/"amd64", can still self-update.
var (
	osAliases = map[string][]string{
		"darwin": {"darwin", "macos"},
	}
	archAliases = map[string][]string{
		"amd64": {"x86_64", "amd64"},
		"386":   {"i386", "386"},
	}
)

// matchAsset returns the index of the first archive in names built for
// goos/goarch, or -1. Matching is case-insensitive and only considers
// .tar.gz and .zip archives, so checksum files and the like are skipped.
func matchAsset(names []string, goos, goarch string) int {
	oses := osAliases[goos]
	if oses == nil {
		oses = []string{goos}
	}
	arches := archAliases[goarch]
	if arches == nil {
		arches = []string{goarch}
	}

	for i, name := range names {
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".zip") {
			continue
		}
		if containsAny(lower, oses) && containsAny(lower, arches) {
			return i
		}
	}
	return -1
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// downloadAndApplyUpdate downloads and applies the update.
// A client with a timeout is used so a slow or malicious CDN cannot hang the
// updater indefinitely.
func (u *AutoUpdater) downloadAndApplyUpdate(url, assetName string) error {
	log.Printf("Downloading update from %s", url)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
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

// newestRelease is the release to update to. Stable builds follow GitHub's
// latest release, which skips prereleases. A prerelease build (0.3.0-rc.1)
// opted in to previews: it follows the newest release of any kind, so it
// gets the next rc, and the stable release once that is out.
func (u *AutoUpdater) newestRelease(ctx context.Context) (*github.RepositoryRelease, error) {
	if !isPrerelease(u.options.CurrentVersion) {
		release, _, err := u.client.Repositories.GetLatestRelease(ctx, githubOwner, githubRepo)
		return release, err
	}
	releases, _, err := u.client.Repositories.ListReleases(ctx, githubOwner, githubRepo, &github.ListOptions{PerPage: 50})
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(releases))
	byTag := map[string]*github.RepositoryRelease{}
	for _, r := range releases {
		if r.GetDraft() {
			continue
		}
		tags = append(tags, r.GetTagName())
		byTag[r.GetTagName()] = r
	}
	newest := pickNewest(tags, true)
	if newest == "" {
		return nil, fmt.Errorf("no releases found")
	}
	return byTag[newest], nil
}

// pickNewest returns the highest semantic version among tags, skipping
// prereleases unless asked for and tags that are not versions.
func pickNewest(tags []string, prereleases bool) string {
	best := ""
	for _, t := range tags {
		if _, _, ok := parseVersion(t); !ok || (!prereleases && isPrerelease(t)) {
			continue
		}
		if best == "" {
			best = t
			continue
		}
		if c, _ := compareVersions(t, best); c > 0 {
			best = t
		}
	}
	return best
}
