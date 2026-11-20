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

// OpencodeWarnings lists user-level opencode configs that may override the
// one at globalPath: sibling files and the OPENCODE_CONFIG* variables.
// Project configs are not checked: which apply depends on where opencode runs.
func OpencodeWarnings(globalPath string) []string {
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
	return w
}

// PiWarnings lists user-level pi configs that may override the goophy
// provider: a goophy provider in models.json.
func PiWarnings(agentDir string) []string {
	var w []string
	p := filepath.Join(agentDir, "models.json")
	if cfg, _, err := readJSONC(p); err == nil {
		if _, ok := asMap(cfg["providers"])[ProviderID]; ok {
			w = append(w, fmt.Sprintf("%s defines a %q provider; pi applies it on top of goophy's extension. Remove it unless you need it",
				tilde(p), ProviderID))
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

// tilde shortens a path in the home directory to ~/...
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}
