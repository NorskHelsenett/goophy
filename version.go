// Package goophy exposes build metadata shared across the binary, such as the
// changelog-derived version.
package goophy

import (
	_ "embed"
	"regexp"
	"strings"
)

// Changelog is the embedded contents of CHANGELOG.md.
//
//go:embed CHANGELOG.md
var Changelog string

// versionHeading matches a "## [1.2.3]" / "## v1.2.3" style changelog heading
// and captures the version, including any pre-release suffix.
var versionHeading = regexp.MustCompile(`(?m)^##\s*\[?v?([0-9]+\.[0-9]+\.[0-9]+[^\]\s]*)\]?`)

// ChangelogVersion returns the most recent version documented in CHANGELOG.md,
// or an empty string if none can be found.
func ChangelogVersion() string {
	if m := versionHeading.FindStringSubmatch(Changelog); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
