package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	// Only Ollama and unknown upstreams auto-pull; Open WebUI serves /api/pull
	// only under /ollama, and vLLM/OpenAI can't pull at all.
	cases := map[UpstreamKind]bool{
		UpstreamOllama:    true,
		UpstreamOpenWebUI: false,
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
		UpstreamUnknown:   "/v1", // unidentified upstreams default to the standard OpenAI prefix
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
		// Catch-all hosts (SPA servers, gateways) answer every path with 200 HTML;
		// the JSON endpoints must not classify on status alone.
		{"api config html is not decisive", UpstreamOpenWebUI, "/api/config", "nginx", "<!doctype html>", UpstreamUnknown},
		{"api tags html is not decisive", UpstreamOllama, "/api/tags", "nginx", "<!doctype html>", UpstreamUnknown},
		{"api version html is not decisive", UpstreamOllama, "/api/version", "", "<html></html>", UpstreamUnknown},
		{"v1 models html is not decisive", UpstreamOpenAI, "/v1/models", "nginx", "<!doctype html>", UpstreamUnknown},
		{"api models list is openwebui", UpstreamOpenWebUI, "/api/models", "uvicorn", `{"data":[]}`, UpstreamOpenWebUI},
		{"api models html is not decisive", UpstreamOpenWebUI, "/api/models", "nginx", "<!doctype html>", UpstreamUnknown},
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
		// An unidentified upstream (e.g. Ollama not up at startup) still routes bare
		// OpenAI paths to /v1 so they don't 404, but leaves native paths alone.
		{"unknown upstream routes to v1", UpstreamUnknown, "/chat/completions", "/v1/chat/completions", true},
		{"unknown native chat untouched", UpstreamUnknown, "/api/chat", "/api/chat", false},
		// A path that merely shares a suffix must NOT be routed: Ollama-native
		// /api/embeddings stays put (only /v1 or bare is OpenAI-style on Ollama)...
		{"ollama native embeddings untouched", UpstreamOllama, "/api/embeddings", "/api/embeddings", false},
		// ...but on Open WebUI /api IS the OpenAI prefix, so it routes (idempotently).
		{"openwebui api embeddings routes", UpstreamOpenWebUI, "/api/embeddings", "/api/embeddings", true},
		// A client /ollama prefix is not an OpenAI prefix; leave it for rewritePath.
		{"ollama-prefixed embeddings untouched", UpstreamOllama, "/ollama/api/embeddings", "/ollama/api/embeddings", false},
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
	p := &OllamaProxy{targetURL: u}
	p.upstream.Store(int32(kind))
	return p
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
		// A base ending in an OpenAI prefix names the prefix explicitly, even when
		// detection picked a different one (or none).
		{"unknown base api uses api", "http://h/api", UpstreamUnknown, "/chat/completions", "/api/chat/completions", "/api/chat/completions"},
		{"openwebui base v1 uses v1", "http://h/v1", UpstreamOpenWebUI, "/chat/completions", "/v1/chat/completions", "/v1/chat/completions"},
		{"gateway base v1 uses v1", "http://h/gw/v1", UpstreamVLLM, "/v1/embeddings", "/gw/v1/v1/embeddings", "/gw/v1/embeddings"},
		// A non-matching base path is preserved and prepended.
		{"gateway base preserved", "http://h/gateway", UpstreamVLLM, "/chat/completions", "/gateway/chat/completions", "/gateway/v1/chat/completions"},
		{"ollama base keeps v1", "http://h/ollama", UpstreamOllama, "/chat/completions", "/ollama/chat/completions", "/ollama/v1/chat/completions"},
		// Non-OpenAI path falls back to rewritePath (which returns the director path here).
		{"ollama native falls back", "http://h", UpstreamOllama, "/api/chat", "/api/chat", "/api/chat"},
		// Unidentified upstream still routes OpenAI paths to /v1 (see openAIPrefix).
		{"unknown upstream routes to v1", "http://h", UpstreamUnknown, "/chat/completions", "/chat/completions", "/v1/chat/completions"},
		{"unknown native falls back", "http://h", UpstreamUnknown, "/api/chat", "/api/chat", "/api/chat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProxy(t, tc.target, tc.kind)
			got := p.resolvePath(tc.kind, tc.origPath, tc.directorArg)
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

// TestProxyRoutesOpenAIPathUnknownUpstream reproduces the "Ollama not up at
// startup" case: detection failed so the proxy is built with UpstreamUnknown, and
// a client still POSTs the bare OpenAI path /chat/completions. The upstream must
// receive it rewritten to /v1/chat/completions rather than a bare path that 404s.
func TestProxyRoutesOpenAIPathUnknownUpstream(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamUnknown)
	if err != nil {
		t.Fatalf("NewOllamaProxy: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	p.handle(p.newReverseProxy())(rec, req)

	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream received path %q, want /v1/chat/completions", gotPath)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("response status = %d, want 200", rec.Code)
	}
}

func TestDetectUpstreamHonorsBasePath(t *testing.T) {
	// Upstream served under a subpath: only /ollama/* is answered, host-root 404s.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ollama/" {
			_, _ = w.Write([]byte("Ollama is running"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := DetectUpstream(context.Background(), srv.URL+"/ollama", "")
	if err != nil {
		t.Fatalf("DetectUpstream returned error: %v", err)
	}
	if got != UpstreamOllama {
		t.Errorf("DetectUpstream = %s, want Ollama", got)
	}
}

func TestDetectUpstreamAuthRequired(t *testing.T) {
	// Every probe is rejected with 401: reachable but the key is missing/wrong.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	kind, err := DetectUpstream(context.Background(), srv.URL, "")
	if !errors.Is(err, ErrUpstreamAuth) {
		t.Fatalf("DetectUpstream error = %v, want ErrUpstreamAuth", err)
	}
	if kind != UpstreamUnknown {
		t.Errorf("DetectUpstream kind = %s, want unknown", kind)
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

func TestDetectUpstreamMixedAuthIsNotFatal(t *testing.T) {
	// vLLM started with --api-key guards /v1 but answers /health openly. Without
	// API_KEY (clients bring their own), that's not a configuration error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusUnauthorized)
		case "/health":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	kind, err := DetectUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("DetectUpstream error = %v, want nil", err)
	}
	if kind != UpstreamUnknown {
		t.Errorf("DetectUpstream = %s, want unknown", kind)
	}
}

func TestDetectUpstreamCatchAllHTMLIsNotOpenWebUI(t *testing.T) {
	// A host that answers every path with 200 HTML must not be classified.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer srv.Close()

	kind, err := DetectUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("DetectUpstream returned error: %v", err)
	}
	if kind != UpstreamUnknown {
		t.Errorf("DetectUpstream = %s, want unknown", kind)
	}
}

func TestDetectUpstreamStripsOpenAIPrefixFromBase(t *testing.T) {
	// API_ENDPOINT=http://owui/api must probe /api/config, not /api/api/config.
	srv := httptest.NewServer(upstreamHandler(UpstreamOpenWebUI))
	defer srv.Close()

	got, err := DetectUpstream(context.Background(), srv.URL+"/api", "")
	if err != nil {
		t.Fatalf("DetectUpstream returned error: %v", err)
	}
	if got != UpstreamOpenWebUI {
		t.Errorf("DetectUpstream = %s, want Open WebUI", got)
	}
}

func TestDetectUpstreamStopsAtFirstConnectionFailure(t *testing.T) {
	// A host that accepts and drops every connection never answers; the sweep
	// must give up after the first probe instead of trying (and timing out on)
	// every path.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, len(healthProbes)*2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			conn.Close()
		}
	}()

	if _, err := DetectUpstream(context.Background(), "http://"+ln.Addr().String(), ""); err == nil {
		t.Fatal("DetectUpstream on dropping host = nil error, want error")
	}
	// The client may retry an idempotent GET once on a fresh connection.
	if n := len(accepted); n > 2 {
		t.Errorf("DetectUpstream opened %d connections, want at most 2 (one probe)", n)
	}
}

func TestUpstreamKindRedetectsWhenUnknown(t *testing.T) {
	// Upstream unreachable at startup, then comes up: the proxy must identify it.
	srv := httptest.NewServer(upstreamHandler(UpstreamOpenWebUI))
	defer srv.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", srv.URL, "", UpstreamUnknown)
	if err != nil {
		t.Fatalf("NewOllamaProxy: %v", err)
	}
	if got := p.upstreamKind(); got != UpstreamUnknown {
		t.Fatalf("upstreamKind within throttle window = %s, want unknown", got)
	}
	p.lastDetect = time.Now().Add(-redetectInterval)
	if got := p.upstreamKind(); got != UpstreamOpenWebUI {
		t.Fatalf("upstreamKind after throttle = %s, want Open WebUI", got)
	}
}

func TestBarePathBodyIsNormalized(t *testing.T) {
	// A bare /chat/completions must get the same openai/ stripping as /v1/...
	var gotPath, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamVLLM)
	if err != nil {
		t.Fatalf("NewOllamaProxy: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"openai/llama3"}`))
	p.handle(p.newReverseProxy())(httptest.NewRecorder(), req)

	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
	if gotBody != `{"model":"llama3"}` {
		t.Errorf("upstream body = %s, want model llama3", gotBody)
	}
}
