package clientcfg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	in := `{
  // comment
  "a": "http://x//y", /* block */
  "b": [1, 2,],
  "c": {"d": "\"q,}"},
},
`
	var v map[string]any
	if err := json.Unmarshal(StripJSONC([]byte(in)), &v); err != nil {
		t.Fatalf("%v\n%s", err, StripJSONC([]byte(in)))
	}
	if v["a"] != "http://x//y" {
		t.Errorf("a = %v", v["a"])
	}
	if v["c"].(map[string]any)["d"] != `"q,}` {
		t.Errorf("c.d = %v", v["c"])
	}
}

func TestConfigureOpencodeMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	orig := `{
  "lsp": true,
  "provider": {
    "goophy": {"name": "Mine", "models": {"nhn-large": {"name": "nhn-large:fast", "limit": {"context": 8000}}}},
    "other": {"name": "x"}
  },
}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	o := Options{BaseURL: "http://localhost:1/v1", DefaultModel: "nhn-coder", Models: []Model{{ID: "nhn-large"}, {ID: "nhn-medium", ContextWindow: 1000}}}
	r, err := ConfigureOpencode(path, o)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed || r.Backup == "" {
		t.Fatalf("result %+v", r)
	}
	if b, _ := os.ReadFile(r.Backup); string(b) != orig {
		t.Error("backup does not hold the original")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	var cfg map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["lsp"] != true || cfg["autoupdate"] != false || cfg["share"] != "disabled" || cfg["model"] != "goophy/nhn-coder" {
		t.Errorf("top-level: %v", cfg)
	}
	if ep := cfg["enabled_providers"].([]any); len(ep) != 1 || ep[0] != "goophy" {
		t.Errorf("enabled_providers %v", ep)
	}
	prov := cfg["provider"].(map[string]any)
	if _, ok := prov["other"]; !ok {
		t.Error("other provider dropped")
	}
	g := prov["goophy"].(map[string]any)
	if g["name"] != "Mine" || g["options"].(map[string]any)["baseURL"] != "http://localhost:1/v1" {
		t.Errorf("goophy provider %v", g)
	}
	models := g["models"].(map[string]any)
	if models["nhn-large"].(map[string]any)["name"] != "nhn-large:fast" {
		t.Error("user model tweak overwritten")
	}
	for _, id := range []string{"nhn-coder", "nhn-medium"} {
		if _, ok := models[id]; !ok {
			t.Errorf("model %s missing", id)
		}
	}

	// A second run is a no-op.
	r, err = ConfigureOpencode(path, o)
	if err != nil || r.Changed {
		t.Errorf("second run: %+v %v", r, err)
	}
}

func TestConfigurePi(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"light","defaultModel":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := ConfigurePi(dir, Options{BaseURL: "http://localhost:9/v1/", DefaultModel: "nhn-coder"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Backup != "" || rs[1].Backup == "" {
		t.Fatalf("results %+v", rs)
	}
	ext, _ := os.ReadFile(filepath.Join(dir, "extensions", "goophy.ts"))
	if strings.Contains(string(ext), "{{") || !strings.Contains(string(ext), `const BASE_URL = "http://localhost:9/v1";`) || !strings.Contains(string(ext), `const DEFAULT_MODEL = "nhn-coder";`) {
		t.Errorf("extension not rendered:\n%s", ext)
	}
	var s map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	_ = json.Unmarshal(b, &s)
	if s["theme"] != "light" || s["defaultProvider"] != "goophy" || s["defaultModel"] != "nhn-coder" || s["enableInstallTelemetry"] != false {
		t.Errorf("settings %v", s)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	rs, err := ConfigurePi(dir, Options{BaseURL: "http://l/v1", DefaultModel: "m", DryRun: true})
	if err != nil || !rs[0].Changed {
		t.Fatalf("%+v %v", rs, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run wrote %v", entries)
	}
}

func TestDiscoverModelsFiltersNonChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nhn-coder","max_model_len":262144},{"id":"bge-reranker"},{"id":"nomic-embed-text"},{"id":"text-embedding-3"}]}`))
	}))
	defer srv.Close()
	ms, err := DiscoverModels(context.Background(), srv.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "nhn-coder" || ms[0].ContextWindow != 262144 {
		t.Errorf("%+v", ms)
	}
}

func TestBackupKeepsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"orig":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if _, err := ConfigurePi(dir, Options{BaseURL: "http://l/v1", DefaultModel: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigurePi(dir, Options{BaseURL: "http://l/v1", DefaultModel: "b"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path + ".bak"); string(b) != `{"orig":1}` {
		t.Errorf("backup = %s", b)
	}
}

func TestOpencodeBackfillsLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	// First run while goophy is down: no limits known.
	if _, err := ConfigureOpencode(path, Options{BaseURL: "http://l/v1", DefaultModel: "nhn-coder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureOpencode(path, Options{BaseURL: "http://l/v1", DefaultModel: "nhn-coder", Models: []Model{{ID: "nhn-coder", ContextWindow: 262144}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"context": 262144`) || !strings.Contains(string(b), `"output": 32768`) {
		t.Errorf("limit not backfilled:\n%s", b)
	}
}

func TestWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles.json")
	link := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(real, []byte(`{"lsp":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if _, err := ConfigureOpencode(link, Options{BaseURL: "http://l/v1", DefaultModel: "m"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced by a file")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), `"model": "goophy/m"`) {
		t.Errorf("target not updated:\n%s", b)
	}
}

func TestRejectsNonObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureOpencode(path, Options{BaseURL: "http://l/v1", DefaultModel: "m"}); err == nil {
		t.Error("want an error for a null config")
	}
}

func TestKeepsHTMLAndBigNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, []byte(`{"x":"a && b < c","n":9007199254740993}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureOpencode(path, Options{BaseURL: "http://l/v1", DefaultModel: "m"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"a && b < c"`) || !strings.Contains(string(b), "9007199254740993") {
		t.Errorf("rewritten lossy:\n%s", b)
	}
}
