// Package clientcfg points coding agents (opencode, pi) at a local goophy.
//
// Both writers merge into the user's existing config: keys goophy does not
// own are kept, and the file as it was before goophy first changed it is
// saved next to it as <name>.bak.
package clientcfg

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ProviderID is the provider name both clients get for goophy.
const ProviderID = "goophy"

// Options says where goophy listens and which model to make the default.
type Options struct {
	// BaseURL is goophy's OpenAI-compatible base, e.g. http://localhost:22434/v1.
	BaseURL string
	// DefaultModel is the model the client starts with.
	DefaultModel string
	// Models are the chat models to list. Empty means FallbackModels.
	Models []Model
	// DryRun reports what would change without writing anything.
	DryRun bool
}

// Model is one entry of the catalog written into a client config.
type Model struct {
	ID            string
	ContextWindow int // 0 when unknown
}

// FallbackModels is used when goophy is not running at configure time.
var FallbackModels = []Model{
	{ID: "nhn-coder"},
	{ID: "nhn-medium"},
	{ID: "nhn-large"},
}

// Result describes what a writer did to one file.
type Result struct {
	Path    string
	Changed bool
	Backup  string // "" when there was nothing to back up
	// BackupKept is set when Backup already existed and was left as is: it
	// holds the file from before goophy first changed it.
	BackupKept bool
}

var nonChat = regexp.MustCompile(`(?i)(embedding|reranker|embed-)`)

// DiscoverModels asks a running goophy for its chat models.
func DiscoverModels(ctx context.Context, baseURL string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models: %s", res.Status)
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET /v1/models: %w", err)
	}
	var out []Model
	for _, m := range body.Data {
		if m.ID != "" && !nonChat.MatchString(m.ID) {
			out = append(out, Model{ID: m.ID, ContextWindow: m.MaxModelLen})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("goophy listed no chat models")
	}
	return out, nil
}

// outputCap leaves room for input: a quarter of the context, at most 32k.
func outputCap(ctx int) int {
	return min(ctx/4, 32768)
}

// catalog returns the catalog with the default model guaranteed present.
func (o Options) catalog() []Model {
	models := o.Models
	if len(models) == 0 {
		models = FallbackModels
	}
	for _, m := range models {
		if m.ID == o.DefaultModel {
			return models
		}
	}
	return append([]Model{{ID: o.DefaultModel}}, models...)
}

// ---- opencode -------------------------------------------------------------

// OpencodeConfigPath is ~/.config/opencode/opencode.json (XDG_CONFIG_HOME
// respected), on every OS, as opencode itself resolves it.
func OpencodeConfigPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "opencode", "opencode.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

// ConfigureOpencode merges goophy into opencode.json and switches off
// opencode's network features: update checks, session sharing to opencode.ai
// and every provider other than goophy (so cloud keys in auth.json are
// ignored).
func ConfigureOpencode(path string, o Options) (Result, error) {
	cfg, old, err := readJSONC(path)
	if err != nil {
		return Result{Path: path}, err
	}
	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	cfg["autoupdate"] = false
	cfg["share"] = "disabled"
	cfg["enabled_providers"] = []any{ProviderID}
	cfg["model"] = ProviderID + "/" + o.DefaultModel

	providers := asMap(cfg["provider"])
	p := asMap(providers[ProviderID])
	p["npm"] = "@ai-sdk/openai-compatible"
	if _, ok := p["name"]; !ok {
		p["name"] = "Goophy"
	}
	opts := asMap(p["options"])
	opts["baseURL"] = o.BaseURL
	p["options"] = opts
	// Keep the user's per-model tweaks (limits, names); add what is missing.
	models := asMap(p["models"])
	for _, m := range o.catalog() {
		entry, ok := models[m.ID].(map[string]any)
		if !ok {
			entry = map[string]any{"name": m.ID}
		}
		// Fill in limits learnt from a running goophy, without touching
		// limits the user set.
		if _, has := entry["limit"]; !has && m.ContextWindow > 0 {
			entry["limit"] = map[string]any{"context": m.ContextWindow, "output": outputCap(m.ContextWindow)}
		}
		models[m.ID] = entry
	}
	p["models"] = models
	providers[ProviderID] = p
	cfg["provider"] = providers

	data, err := marshal(cfg)
	if err != nil {
		return Result{Path: path}, err
	}
	return writeWithBackup(path, old, data, o.DryRun)
}

// ---- pi -------------------------------------------------------------------

//go:embed pi_extension.ts
var piExtension string

// PiAgentDir is ~/.pi/agent, or $PI_CODING_AGENT_DIR when set.
func PiAgentDir() (string, error) {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// PiExtension renders the goophy provider extension for pi.
func PiExtension(o Options) string {
	return strings.NewReplacer(
		"{{BASE_URL}}", strings.TrimRight(o.BaseURL, "/"),
		"{{DEFAULT_MODEL}}", o.DefaultModel,
	).Replace(piExtension)
}

// ConfigurePi installs the goophy provider extension (it discovers models
// from goophy at startup), makes goophy/<model> pi's default and turns off
// pi's telemetry.
func ConfigurePi(dir string, o Options) ([]Result, error) {
	extPath := filepath.Join(dir, "extensions", "goophy.ts")
	setPath := filepath.Join(dir, "settings.json")

	// Parse settings first, so a broken settings.json leaves nothing half
	// installed.
	settings, oldSet, err := readJSONC(setPath)
	if err != nil {
		return nil, err
	}
	settings["defaultProvider"] = ProviderID
	settings["defaultModel"] = o.DefaultModel
	// No install/update reporting or analytics. pi's pi.dev version check has
	// no setting; it is switched off with PI_SKIP_VERSION_CHECK=1.
	settings["enableInstallTelemetry"] = false
	settings["enableAnalytics"] = false
	setData, err := marshal(settings)
	if err != nil {
		return nil, err
	}
	oldExt, err := readOptional(extPath)
	if err != nil {
		return nil, err
	}

	ext, err := writeWithBackup(extPath, oldExt, []byte(PiExtension(o)), o.DryRun)
	if err != nil {
		return []Result{ext}, err
	}
	set, err := writeWithBackup(setPath, oldSet, setData, o.DryRun)
	return []Result{ext, set}, err
}

// ---- helpers --------------------------------------------------------------

// marshal writes indented JSON as a person would: "<" and "&" kept as is.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// readJSONC reads a JSON object that may carry comments and trailing commas,
// as opencode's config may. A missing file is an empty object.
func readJSONC(path string) (map[string]any, []byte, error) {
	raw, err := readOptional(path)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return cfg, raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(StripJSONC(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))))
	dec.UseNumber() // keep large integers exact
	if err := dec.Decode(&cfg); err != nil {
		return nil, raw, fmt.Errorf("%s: %w", path, err)
	}
	if cfg == nil { // the file held null
		return nil, raw, fmt.Errorf("%s: not a JSON object", path)
	}
	return cfg, raw, nil
}

// StripJSONC removes // and /* */ comments and trailing commas outside
// strings, leaving plain JSON.
func StripJSONC(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inStr, esc := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			if i < len(in) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++ // land on '/'
		case c == ']' || c == '}':
			// Drop a trailing comma before the closer.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	// A trailing comma after the last closer (e.g. "{...},\n") is not valid.
	trimmed := bytes.TrimRight(out, " \t\r\n")
	if bytes.HasSuffix(trimmed, []byte(",")) {
		out = trimmed[:len(trimmed)-1]
	}
	return out
}

// writeWithBackup writes data to path unless it already holds exactly that,
// saving the previous content as path+".bak" first unless a backup exists.
func writeWithBackup(path string, old, data []byte, dryRun bool) (Result, error) {
	r := Result{Path: path}
	if old != nil && bytes.Equal(old, data) {
		return r, nil
	}
	r.Changed = true
	// Write through a symlink (e.g. a dotfiles repo) instead of replacing it.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if old != nil {
		r.Backup = path + ".bak"
		if _, err := os.Stat(r.Backup); err == nil {
			r.BackupKept = true
		}
	}
	if dryRun {
		return r, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return r, err
	}
	mode := os.FileMode(0o644)
	if old != nil {
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		// Keep the oldest backup: it holds the config from before goophy
		// first touched it, which a rerun must not replace.
		if !r.BackupKept {
			if err := os.WriteFile(r.Backup, old, mode); err != nil {
				return r, fmt.Errorf("backing up %s: %w", path, err)
			}
		}
	}
	tmp := path + ".goophy-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return r, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return r, err
	}
	return r, nil
}
