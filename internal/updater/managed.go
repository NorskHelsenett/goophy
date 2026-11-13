package updater

import (
	"os"
	"path/filepath"
	"strings"
)

// PackageManager reports which package manager installed the running binary,
// or "" when goophy was installed by hand (release archive, go build, ...).
// A managed binary must not replace itself: the package manager would no
// longer know which version is installed, and its next upgrade would clash
// with ours.
func PackageManager() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return packageManagerFor(exe)
}

func packageManagerFor(exe string) string {
	p := filepath.ToSlash(exe)
	if strings.Contains(p, "/Caskroom/") || strings.Contains(p, "/Cellar/") {
		return "Homebrew"
	}
	return ""
}

// UpgradeHint returns the command that upgrades goophy for the given package
// manager.
func UpgradeHint(manager string) string {
	if manager == "Homebrew" {
		return "brew upgrade goophy"
	}
	return "goophy update"
}
