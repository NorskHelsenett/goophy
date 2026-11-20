package clientcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Other config files can override what configure writes. These functions
// point them out; they never change them.

// opencodeKeys are the settings whose override would stop opencode from
// using goophy.
var opencodeKeys = []string{"model", "enabled_providers", "disabled_providers", "provider"}

// OpencodeWarnings lists other opencode configs that may override the global
// one at globalPath: sibling files, the OPENCODE_CONFIG* variables, and
// project configs from cwd up to the repository root.
func OpencodeWarnings(globalPath, cwd string) []string {
	var w []string
	dir := filepath.Dir(globalPath)
	for _, name := range []string{"opencode.jsonc", "config.json"} {
		p := filepath.Join(dir, name)
		if keys := setKeys(p, opencodeKeys); keys != nil {
			w = append(w, fmt.Sprintf("%s also exists and sets %s; opencode merges it with %s",
				tilde(p), strings.Join(keys, ", "), filepath.Base(globalPath)))
		}
	}
	for _, env := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"} {
		if os.Getenv(env) != "" {
			w = append(w, fmt.Sprintf("%s is set; the config it names may override these settings", env))
		}
	}
	for _, d := range upToRepoRoot(cwd) {
		for _, name := range []string{"opencode.json", "opencode.jsonc", filepath.Join(".opencode", "opencode.json"), filepath.Join(".opencode", "opencode.jsonc")} {
			p := filepath.Join(d, name)
			if filepath.Clean(p) == filepath.Clean(globalPath) {
				continue
			}
			if keys := setKeys(p, opencodeKeys); keys != nil {
				w = append(w, fmt.Sprintf("%s sets %s, which overrides these settings in this project",
					tilde(p), strings.Join(keys, ", ")))
			}
		}
	}
	return w
}

// PiWarnings lists pi configs that may override the goophy provider or the
// default model: a goophy provider in models.json, and project settings in
// cwd/.pi.
func PiWarnings(agentDir, cwd string) []string {
	var w []string
	p := filepath.Join(agentDir, "models.json")
	if cfg, _, err := readJSONC(p); err == nil {
		if _, ok := asMap(cfg["providers"])[ProviderID]; ok {
			w = append(w, fmt.Sprintf("%s defines a %q provider; pi applies it on top of goophy's extension. Remove it unless you need it",
				tilde(p), ProviderID))
		}
	}
	proj := filepath.Join(cwd, ".pi", "settings.json")
	if filepath.Clean(proj) != filepath.Clean(filepath.Join(agentDir, "settings.json")) {
		if keys := setKeys(proj, []string{"defaultProvider", "defaultModel"}); keys != nil {
			w = append(w, fmt.Sprintf("%s sets %s, which overrides these settings in this project",
				tilde(proj), strings.Join(keys, ", ")))
		}
	}
	return w
}

// setKeys returns which of keys the JSON(C) file at path sets: nil when the
// file is missing or sets none of them. An unreadable file reports itself.
func setKeys(path string, keys []string) []string {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, _, err := readJSONC(path)
	if err != nil {
		return []string{"(could not be read)"}
	}
	var out []string
	for _, k := range keys {
		if _, ok := cfg[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// upToRepoRoot returns dir and its parents up to the enclosing git
// repository's root, or just dir outside a repository. The home directory
// itself is never included.
func upToRepoRoot(dir string) []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if d == home {
			break
		}
		dirs = append(dirs, d)
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return dirs
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	// Not in a repository: only the directory itself.
	return dirs[:min(1, len(dirs))]
}

// tilde shortens a path in the home directory to ~/...
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}
