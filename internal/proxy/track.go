package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

// trackerKey carries the request's *stats.Tracker through the request context.
type trackerKey struct{}

// trackerFrom returns the tracker for ctx, or nil (whose methods are no-ops).
func trackerFrom(ctx context.Context) *stats.Tracker {
	t, _ := ctx.Value(trackerKey{}).(*stats.Tracker)
	return t
}

// SetCollector enables per-request statistics. With a collector set, goophy
// also asks OpenAI-compatible upstreams to report token usage on streamed
// responses (stream_options.include_usage) and requests uncompressed responses
// for LLM endpoints so usage can be read from them. Without one (the default,
// headless mode) request handling is unchanged.
// Call it before Start.
func (p *OllamaProxy) SetCollector(c *stats.Collector) {
	p.collector = c
}

// maxUsageLine bounds how much of a single line (or a whole non-streamed body)
// the usage scanner buffers. Longer lines are skipped, not forwarded short.
const maxUsageLine = 4 << 20

// trackingWriter wraps the client ResponseWriter to record status, first-byte
// latency, size and token usage. It never alters what the client receives.
type trackingWriter struct {
	http.ResponseWriter
	t           *stats.Tracker
	wroteHeader bool
	scan        *usageScanner // nil when the body isn't inspected
}

func (w *trackingWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		ct, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
		streaming := ct == "text/event-stream" || ct == "application/x-ndjson"
		// Only inspect bodies we can read: uncompressed JSON-ish responses.
		if w.Header().Get("Content-Encoding") == "" &&
			(streaming || ct == "application/json") {
			w.scan = &usageScanner{t: w.t, streaming: streaming}
		}
		w.t.SetStatus(code, streaming)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.t.Wrote(n)
	if w.scan != nil {
		w.scan.feed(b[:n])
	}
	return n, err
}

// Flush supports streaming; ReverseProxy flushes SSE responses per event.
func (w *trackingWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *trackingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish flushes any trailing buffered data through the scanner.
func (w *trackingWriter) finish() {
	if w.scan != nil {
		w.scan.close()
	}
}

// usageScanner splits a response body into lines (SSE "data:" events or
// NDJSON) or, for plain JSON, buffers the body, and extracts token usage from
// OpenAI ("usage") and Ollama ("prompt_eval_count"/"eval_count") payloads.
type usageScanner struct {
	t         *stats.Tracker
	streaming bool
	buf       []byte
	overflow  bool // current line exceeded maxUsageLine; skip to next newline
}

func (s *usageScanner) feed(b []byte) {
	if !s.streaming {
		if len(s.buf)+len(b) > maxUsageLine {
			s.overflow = true
			s.buf = nil
			return
		}
		if !s.overflow {
			s.buf = append(s.buf, b...)
		}
		return
	}
	chunks := 0
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !s.overflow {
				s.buf = append(s.buf, b...)
				if len(s.buf) > maxUsageLine {
					s.buf, s.overflow = nil, true
				}
			}
			break
		}
		if !s.overflow {
			s.buf = append(s.buf, b[:i]...)
			if s.line(s.buf) {
				chunks++
			}
		}
		s.buf, s.overflow = s.buf[:0], false
		b = b[i+1:]
	}
	if chunks > 0 {
		s.t.AddChunks(chunks)
	}
}

// line handles one streamed line and reports whether it was a data event.
func (s *usageScanner) line(l []byte) bool {
	l = bytes.TrimSpace(l)
	if after, ok := bytes.CutPrefix(l, []byte("data:")); ok {
		l = bytes.TrimSpace(after)
		if bytes.Equal(l, []byte("[DONE]")) {
			return false
		}
	}
	if len(l) == 0 || l[0] != '{' {
		return false
	}
	s.parse(l)
	return true
}

func (s *usageScanner) close() {
	if s.overflow || len(s.buf) == 0 {
		return
	}
	if s.streaming {
		if s.line(s.buf) {
			s.t.AddChunks(1)
		}
	} else {
		s.parse(bytes.TrimSpace(s.buf))
	}
	s.buf = nil
}

// usagePayload covers both OpenAI and Ollama usage reporting.
type usagePayload struct {
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		// Rerank responses (vLLM, Jina, Cohere-style) report only a total.
		TotalTokens int `json:"total_tokens"`
		// Responses API naming.
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (s *usageScanner) parse(l []byte) {
	// Cheap pre-filter: most stream chunks carry no usage at all.
	if !bytes.Contains(l, []byte(`"usage"`)) && !bytes.Contains(l, []byte(`"eval_count"`)) {
		return
	}
	var p usagePayload
	if json.Unmarshal(l, &p) != nil {
		return
	}
	u := p.Usage
	switch {
	case u != nil && (u.PromptTokens > 0 || u.CompletionTokens > 0):
		cached := 0
		if u.PromptTokensDetails != nil {
			cached = u.PromptTokensDetails.CachedTokens
		}
		s.t.SetUsage(u.PromptTokens, u.CompletionTokens, cached)
	case u != nil && (u.InputTokens > 0 || u.OutputTokens > 0):
		cached := 0
		if u.InputTokensDetails != nil {
			cached = u.InputTokensDetails.CachedTokens
		}
		s.t.SetUsage(u.InputTokens, u.OutputTokens, cached)
	case u != nil && u.TotalTokens > 0:
		s.t.SetUsage(u.TotalTokens, 0, 0)
	case p.PromptEvalCount > 0 || p.EvalCount > 0:
		s.t.SetUsage(p.PromptEvalCount, p.EvalCount, 0)
	}
}

// isLLMPath reports whether path is a generation endpoint whose response may
// carry token usage.
func isLLMPath(path string) bool {
	return hasPathSuffix(path, []string{"/chat/completions", "/completions", "/api/chat", "/api/generate", "/responses"})
}

// injectStreamUsage adds stream_options.include_usage to streamed OpenAI-style
// completion requests that don't already set stream_options, so the upstream
// reports token usage in its final chunk. Returns the body unchanged otherwise.
func injectStreamUsage(path string, body []byte) ([]byte, bool) {
	if !hasPathSuffix(path, []string{"/chat/completions", "/completions"}) {
		return body, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body, false
	}
	if string(m["stream"]) != "true" {
		return body, false
	}
	if _, ok := m["stream_options"]; ok {
		return body, false
	}
	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// peekPaths are endpoints whose body names a model but which goophy doesn't
// otherwise rewrite (rewriteRequestBody leaves them alone).
var peekPaths = []string{"/embeddings", "/rerank", "/api/embed", "/api/embeddings", "/responses"}

// peekModelBody reads, and puts back unchanged, the body of a request to one
// of peekPaths so its model can be recorded. Returns nil for other requests.
func peekModelBody(r *http.Request) []byte {
	if r.Body == nil || r.Method != http.MethodPost || !hasPathSuffix(r.URL.Path, peekPaths) {
		return nil
	}
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	setRequestBody(r, b) // forward whatever was read, as rewriteRequestBody does
	if err != nil {
		return nil
	}
	return b
}

// requestModel extracts the model (or name) field from a JSON request body.
func requestModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if m.Model != "" {
		return m.Model
	}
	return m.Name
}

// finishError describes why a request ended abnormally, or "" on success.
func finishError(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return "client disconnected"
		}
		return err.Error()
	}
	return ""
}

// trimUA shortens a User-Agent to its first product token for display.
func trimUA(ua string) string {
	if i := strings.IndexByte(ua, ' '); i > 0 {
		return ua[:i]
	}
	return ua
}
