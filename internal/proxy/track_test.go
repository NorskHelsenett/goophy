package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

func TestUsageScannerOpenAIStream(t *testing.T) {
	c := stats.New(stats.Info{})
	tr := c.Begin("POST", "/v1/chat/completions", "")
	s := &usageScanner{t: tr, streaming: true}
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n" +
		"data: [DONE]\n\n"
	// Feed in awkward pieces to exercise line reassembly across writes.
	for i := 0; i < len(body); i += 7 {
		s.feed([]byte(body[i:min(i+7, len(body))]))
	}
	s.close()
	tr.Finish("")

	r := c.Snapshot().Recent[0]
	if !r.HasUsage || r.PromptTokens != 12 || r.CompletionTokens != 3 || r.CachedTokens != 8 {
		t.Errorf("usage = %d/%d (has=%v), want 12/3", r.PromptTokens, r.CompletionTokens, r.HasUsage)
	}
	if r.Chunks != 3 {
		t.Errorf("chunks = %d, want 3 (DONE excluded)", r.Chunks)
	}
}

func TestUsageScannerOllamaNDJSON(t *testing.T) {
	c := stats.New(stats.Info{})
	tr := c.Begin("POST", "/api/chat", "")
	s := &usageScanner{t: tr, streaming: true}
	s.feed([]byte(`{"message":{"content":"a"},"done":false}` + "\n"))
	// Final line without trailing newline is handled on close.
	s.feed([]byte(`{"done":true,"prompt_eval_count":5,"eval_count":9}`))
	s.close()
	tr.Finish("")

	r := c.Snapshot().Recent[0]
	if r.PromptTokens != 5 || r.CompletionTokens != 9 || r.Chunks != 2 {
		t.Errorf("got %d/%d chunks=%d, want 5/9 chunks=2", r.PromptTokens, r.CompletionTokens, r.Chunks)
	}
}

func TestUsageScannerBufferedJSON(t *testing.T) {
	c := stats.New(stats.Info{})
	tr := c.Begin("POST", "/v1/chat/completions", "")
	s := &usageScanner{t: tr}
	s.feed([]byte("{\n  \"choices\": [],\n"))
	s.feed([]byte("  \"usage\": {\"prompt_tokens\": 4, \"completion_tokens\": 6}\n}"))
	s.close()
	tr.Finish("")
	if r := c.Snapshot().Recent[0]; r.PromptTokens != 4 || r.CompletionTokens != 6 {
		t.Errorf("got %d/%d, want 4/6", r.PromptTokens, r.CompletionTokens)
	}
}

func TestInjectStreamUsage(t *testing.T) {
	tests := []struct {
		name, path, body string
		want             bool
	}{
		{"streamed chat", "/v1/chat/completions", `{"model":"m","stream":true}`, true},
		{"bare path", "/chat/completions", `{"model":"m","stream":true}`, true},
		{"not streamed", "/v1/chat/completions", `{"model":"m"}`, false},
		{"already set", "/v1/chat/completions", `{"stream":true,"stream_options":{"include_usage":false}}`, false},
		{"ollama native", "/api/chat", `{"stream":true}`, false},
		{"invalid json", "/v1/chat/completions", `nope`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := injectStreamUsage(tc.path, []byte(tc.body))
			if ok != tc.want {
				t.Fatalf("injected = %v, want %v (%s)", ok, tc.want, out)
			}
			if !ok {
				if string(out) != tc.body {
					t.Errorf("body changed without injection: %s", out)
				}
				return
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatal(err)
			}
			so, _ := m["stream_options"].(map[string]any)
			if so["include_usage"] != true || m["model"] != "m" {
				t.Errorf("body = %s", out)
			}
		})
	}
}

// sseUpstream serves a streamed chat completion and records what it received.
func sseUpstream(t *testing.T, gotBody *string, gotAE *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		*gotAE = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`data: {"choices":[{"delta":{"content":"x"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			`data: [DONE]`,
		} {
			io.WriteString(w, l+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
}

const sseWant = "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

func TestHandleTracksStreamedRequest(t *testing.T) {
	var gotBody, gotAE string
	upstream := sseUpstream(t, &gotBody, &gotAE)
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamVLLM)
	if err != nil {
		t.Fatal(err)
	}
	col := stats.New(stats.Info{})
	p.SetCollector(col)
	front := httptest.NewServer(p.handle(p.newReverseProxy()))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/chat/completions",
		strings.NewReader(`{"model":"coder","stream":true,"messages":[]}`))
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "pi/1.5.0 (darwin)")
	resp, err := http.DefaultTransport.RoundTrip(req) // keep the client's Accept-Encoding as-is
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(got) != sseWant {
		t.Errorf("client body changed:\n%q\nwant\n%q", got, sseWant)
	}
	if !strings.Contains(gotBody, `"include_usage":true`) {
		t.Errorf("upstream body lacks include_usage: %s", gotBody)
	}
	// The client's Accept-Encoding is dropped; Go's transport then negotiates
	// gzip itself and decodes it transparently before the usage scanner.
	if gotAE != "gzip" {
		t.Errorf("upstream Accept-Encoding = %q, want transport-managed gzip", gotAE)
	}

	s := col.Snapshot()
	if len(s.Recent) != 1 {
		t.Fatalf("recent = %+v", s.Recent)
	}
	r := s.Recent[0]
	if r.Model != "coder" || r.Status != 200 || !r.Streaming || r.UpstreamPath != "/v1/chat/completions" {
		t.Errorf("request = %+v", r)
	}
	if r.PromptTokens != 7 || r.CompletionTokens != 2 || r.Chunks != 2 || r.UserAgent != "pi/1.5.0" {
		t.Errorf("usage = %d/%d chunks=%d ua=%q", r.PromptTokens, r.CompletionTokens, r.Chunks, r.UserAgent)
	}
	if r.Bytes != int64(len(sseWant)) {
		t.Errorf("bytes = %d, want %d", r.Bytes, len(sseWant))
	}
}

func TestHandleWithoutCollectorLeavesRequestAlone(t *testing.T) {
	var gotBody, gotAE string
	upstream := sseUpstream(t, &gotBody, &gotAE)
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamVLLM)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p.handle(p.newReverseProxy()))
	defer front.Close()

	body := `{"model":"coder","stream":true}`
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if gotBody != body {
		t.Errorf("headless mode modified the body: %s", gotBody)
	}
	if gotAE != "identity" {
		t.Errorf("headless mode changed Accept-Encoding to %q", gotAE)
	}
}

// TestHandleTracksEmbeddingAndRerankModels checks that requests goophy doesn't
// rewrite still get their model and usage recorded, with the body untouched.
func TestHandleTracksEmbeddingAndRerankModels(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			io.WriteString(w, `{"data":[],"usage":{"prompt_tokens":11,"total_tokens":11}}`)
		default:
			io.WriteString(w, `{"results":[],"usage":{"total_tokens":42}}`)
		}
	}))
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamVLLM)
	if err != nil {
		t.Fatal(err)
	}
	col := stats.New(stats.Info{})
	p.SetCollector(col)
	front := httptest.NewServer(p.handle(p.newReverseProxy()))
	defer front.Close()

	for _, tc := range []struct {
		path, body, model string
		prompt            int
	}{
		{"/v1/embeddings", `{"model":"nhn-embed","input":"hi"}`, "nhn-embed", 11},
		{"/v1/rerank", `{"model":"nhn-rerank","query":"q","documents":["a"]}`, "nhn-rerank", 42},
	} {
		resp, err := http.Post(front.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if gotBody != tc.body {
			t.Errorf("%s: upstream body = %s, want unchanged %s", tc.path, gotBody, tc.body)
		}
		r := col.Snapshot().Recent[0]
		if r.Model != tc.model || r.PromptTokens != tc.prompt || !r.HasUsage {
			t.Errorf("%s: model=%q prompt=%d has=%v, want %q/%d", tc.path, r.Model, r.PromptTokens, r.HasUsage, tc.model, tc.prompt)
		}
	}
	if n := len(col.Snapshot().Models); n != 2 {
		t.Errorf("models tab has %d entries, want 2", n)
	}
}
