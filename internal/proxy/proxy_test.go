package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestUpstreamKindString(t *testing.T) {
	cases := map[UpstreamKind]string{
		UpstreamOllama:    "Ollama",
		UpstreamVLLM:      "vLLM",
		UpstreamOpenWebUI: "Open WebUI",
		UpstreamOpenAI:    "OpenAI-compatible",
		UpstreamUnknown:   "unknown",
		UpstreamKind(999): "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("UpstreamKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestSupportsPull(t *testing.T) {
	// Only positively-identified OpenAI-only servers skip auto-pull; everything
	// else (including unknown) keeps Ollama behavior.
	cases := map[UpstreamKind]bool{
		UpstreamOllama:    true,
		UpstreamOpenWebUI: true,
		UpstreamUnknown:   true,
		UpstreamVLLM:      false,
		UpstreamOpenAI:    false,
	}
	for kind, want := range cases {
		if got := kind.SupportsPull(); got != want {
			t.Errorf("%s.SupportsPull() = %v, want %v", kind, got, want)
		}
	}
}

func TestOpenAIPrefix(t *testing.T) {
	cases := map[UpstreamKind]string{
		UpstreamOpenWebUI: "/api",
		UpstreamVLLM:      "/v1",
		UpstreamOpenAI:    "/v1",
		UpstreamOllama:    "/v1",
		UpstreamUnknown:   "",
	}
	for kind, want := range cases {
		if got := kind.openAIPrefix(); got != want {
			t.Errorf("%s.openAIPrefix() = %q, want %q", kind, got, want)
		}
	}
}

func TestLooksLikePythonServer(t *testing.T) {
	cases := map[string]bool{
		"uvicorn":                     true,
		"uvicorn/0.29.0":              true,
		"starlette":                   true,
		"hypercorn":                   true,
		"":                            false,
		"nginx":                       false,
		"ollama":                      false,
		"basehttp/0.6 python/3.12.13": false, // stdlib http.server is not an ASGI stack
	}
	for server, want := range cases {
		if got := looksLikePythonServer(server); got != want {
			t.Errorf("looksLikePythonServer(%q) = %v, want %v", server, got, want)
		}
	}
}

func TestClassifyUpstream(t *testing.T) {
	tests := []struct {
		name   string
		hint   UpstreamKind
		path   string
		server string
		body   string
		want   UpstreamKind
	}{
		{"root banner is ollama", UpstreamUnknown, "/", "", "Ollama is running", UpstreamOllama},
		{"root html is not decisive", UpstreamUnknown, "/", "uvicorn", "<!doctype html>", UpstreamUnknown},
		{"api config is openwebui", UpstreamOpenWebUI, "/api/config", "uvicorn", `{"name":"Open WebUI"}`, UpstreamOpenWebUI},
		{"v1 models with owned_by vllm", UpstreamOpenAI, "/v1/models", "uvicorn", `{"data":[{"id":"x","owned_by":"vllm"}]}`, UpstreamVLLM},
		{"v1 models uvicorn no owned_by is vllm", UpstreamOpenAI, "/v1/models", "uvicorn", `{"data":[{"id":"x"}]}`, UpstreamVLLM},
		{"v1 models plain is generic openai", UpstreamOpenAI, "/v1/models", "nginx", `{"data":[{"id":"x"}]}`, UpstreamOpenAI},
		{"api version no python header is ollama", UpstreamOllama, "/api/version", "", `{"version":"0.5.0"}`, UpstreamOllama},
		{"api version uvicorn is openwebui", UpstreamOllama, "/api/version", "uvicorn", `{"version":"0.5.0"}`, UpstreamOpenWebUI},
		{"api tags no python header is ollama", UpstreamOllama, "/api/tags", "", `{"models":[]}`, UpstreamOllama},
		{"api tags uvicorn is openwebui", UpstreamOllama, "/api/tags", "uvicorn", `{"models":[]}`, UpstreamOpenWebUI},
		{"health status true is openwebui", UpstreamUnknown, "/health", "uvicorn", `{"status":true}`, UpstreamOpenWebUI},
		{"health empty is unknown", UpstreamUnknown, "/health", "uvicorn", "", UpstreamUnknown},
		{"owned_by check tolerates spaces", UpstreamOpenAI, "/v1/models", "nginx", `{"data":[{"owned_by": "vllm"}]}`, UpstreamVLLM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := http.Header{}
			if tc.server != "" {
				hdr.Set("Server", tc.server)
			}
			got := classifyUpstream(tc.hint, tc.path, hdr, []byte(tc.body))
			if got != tc.want {
				t.Errorf("classifyUpstream(%v, %q, Server=%q, %q) = %s, want %s",
					tc.hint, tc.path, tc.server, tc.body, got, tc.want)
			}
		})
	}
}

func TestRouteOpenAIPath(t *testing.T) {
	tests := []struct {
		name     string
		kind     UpstreamKind
		path     string
		wantPath string
		wantOK   bool
	}{
		{"openwebui bare chat", UpstreamOpenWebUI, "/chat/completions", "/api/chat/completions", true},
		{"vllm bare chat", UpstreamVLLM, "/chat/completions", "/v1/chat/completions", true},
		{"ollama bare chat", UpstreamOllama, "/chat/completions", "/v1/chat/completions", true},
		{"openai bare chat", UpstreamOpenAI, "/chat/completions", "/v1/chat/completions", true},
		// Already-prefixed paths normalize to the same canonical target (idempotent).
		{"vllm already v1", UpstreamVLLM, "/v1/chat/completions", "/v1/chat/completions", true},
		{"openwebui v1 to api", UpstreamOpenWebUI, "/v1/chat/completions", "/api/chat/completions", true},
		// /chat/completions must win over the /completions suffix.
		{"chat wins over completions", UpstreamVLLM, "/chat/completions", "/v1/chat/completions", true},
		{"legacy completions", UpstreamVLLM, "/completions", "/v1/completions", true},
		{"models", UpstreamOpenWebUI, "/models", "/api/models", true},
		{"embeddings", UpstreamVLLM, "/embeddings", "/v1/embeddings", true},
		{"rerank", UpstreamVLLM, "/rerank", "/v1/rerank", true},
		// Non-OpenAI and unknown paths are left alone.
		{"ollama native chat untouched", UpstreamOllama, "/api/chat", "/api/chat", false},
		{"ollama native generate untouched", UpstreamOllama, "/api/generate", "/api/generate", false},
		{"unknown upstream never routes", UpstreamUnknown, "/chat/completions", "/chat/completions", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotOK := routeOpenAIPath(tc.kind, tc.path)
			if gotPath != tc.wantPath || gotOK != tc.wantOK {
				t.Errorf("routeOpenAIPath(%s, %q) = (%q, %v), want (%q, %v)",
					tc.kind, tc.path, gotPath, gotOK, tc.wantPath, tc.wantOK)
			}
		})
	}
}

func newTestProxy(t *testing.T, target string, kind UpstreamKind) *OllamaProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parsing target %q: %v", target, err)
	}
	return &OllamaProxy{targetURL: u, upstream: kind}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		kind        UpstreamKind
		origPath    string
		directorArg string // path the default director would have produced (fallback only)
		want        string
	}{
		{"openwebui host only", "http://h", UpstreamOpenWebUI, "/chat/completions", "/chat/completions", "/api/chat/completions"},
		{"vllm host only", "http://h", UpstreamVLLM, "/chat/completions", "/chat/completions", "/v1/chat/completions"},
		// Explicit base path in API_ENDPOINT should not be doubled.
		{"vllm base v1 no double", "http://h/v1", UpstreamVLLM, "/chat/completions", "/v1/chat/completions", "/v1/chat/completions"},
		{"openwebui base api no double", "http://h/api", UpstreamOpenWebUI, "/chat/completions", "/api/chat/completions", "/api/chat/completions"},
		// A non-matching base path is preserved and prepended.
		{"gateway base preserved", "http://h/gateway", UpstreamVLLM, "/chat/completions", "/gateway/chat/completions", "/gateway/v1/chat/completions"},
		// Non-OpenAI path falls back to rewritePath (which returns the director path here).
		{"ollama native falls back", "http://h", UpstreamOllama, "/api/chat", "/api/chat", "/api/chat"},
		{"unknown upstream falls back", "http://h", UpstreamUnknown, "/chat/completions", "/chat/completions", "/chat/completions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProxy(t, tc.target, tc.kind)
			got := p.resolvePath(tc.origPath, tc.directorArg)
			if got != tc.want {
				t.Errorf("resolvePath(%q, %q) [target=%s kind=%s] = %q, want %q",
					tc.origPath, tc.directorArg, tc.target, tc.kind, got, tc.want)
			}
		})
	}
}

// upstreamHandler returns an httptest handler that mimics the given upstream flavor.
func upstreamHandler(kind UpstreamKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch kind {
		case UpstreamOllama:
			switch r.URL.Path {
			case "/":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("Ollama is running"))
			case "/api/tags":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"models":[]}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case UpstreamVLLM:
			w.Header().Set("Server", "uvicorn")
			switch r.URL.Path {
			case "/v1/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"m","owned_by":"vllm"}]}`))
			case "/health":
				w.WriteHeader(http.StatusOK) // empty body
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case UpstreamOpenWebUI:
			w.Header().Set("Server", "uvicorn")
			switch r.URL.Path {
			case "/api/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"name":"Open WebUI"}`))
			case "/api/models":
				w.WriteHeader(http.StatusUnauthorized) // needs auth, but reachable
			case "/health":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":true}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}
}

func TestDetectUpstream(t *testing.T) {
	for _, kind := range []UpstreamKind{UpstreamOllama, UpstreamVLLM, UpstreamOpenWebUI} {
		t.Run(kind.String(), func(t *testing.T) {
			srv := httptest.NewServer(upstreamHandler(kind))
			defer srv.Close()

			got, err := DetectUpstream(context.Background(), srv.URL, "")
			if err != nil {
				t.Fatalf("DetectUpstream returned error: %v", err)
			}
			if got != kind {
				t.Errorf("DetectUpstream = %s, want %s", got, kind)
			}
		})
	}
}

func TestDetectUpstreamUnreachable(t *testing.T) {
	// A server that is immediately closed yields a connection error on every probe.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	kind, err := DetectUpstream(context.Background(), addr, "")
	if err == nil {
		t.Fatalf("DetectUpstream on closed server = %s, want error", kind)
	}
	if kind != UpstreamUnknown {
		t.Errorf("DetectUpstream on closed server kind = %s, want unknown", kind)
	}
}

func TestDetectUpstreamReachableButUnknown(t *testing.T) {
	// Server responds but to nothing we recognize: reachable, no error, unknown kind.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	kind, err := DetectUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("DetectUpstream returned error for reachable server: %v", err)
	}
	if kind != UpstreamUnknown {
		t.Errorf("DetectUpstream = %s, want unknown", kind)
	}
}

func TestDetectUpstreamCancelledContext(t *testing.T) {
	srv := httptest.NewServer(upstreamHandler(UpstreamOllama))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	if _, err := DetectUpstream(ctx, srv.URL, ""); err == nil {
		t.Fatal("DetectUpstream with cancelled context = nil error, want error")
	}
}
