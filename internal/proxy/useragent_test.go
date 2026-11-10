package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserAgentTransport(t *testing.T) {
	old := UserAgent
	UserAgent = "goophy/9.9.9"
	defer func() { UserAgent = old }()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()
	client := newCustomTransport(defaultMaxRedirects).client

	tests := []struct{ name, clientUA, want string }{
		{"appends client agent", "pi/1.2", "goophy/9.9.9 pi/1.2"},
		{"no client agent", "", "goophy/9.9.9"},
		{"already prefixed", "goophy/9.9.9 pi/1.2", "goophy/9.9.9 pi/1.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			if tt.clientUA != "" {
				req.Header.Set("User-Agent", tt.clientUA)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got != tt.want {
				t.Fatalf("User-Agent = %q, want %q", got, tt.want)
			}
			if req.Header.Get("User-Agent") != tt.clientUA {
				t.Fatal("transport mutated the caller's request headers")
			}
		})
	}
}

// End to end: a client request through the reverse proxy reaches the upstream
// with goophy's agent in front of the client's.
func TestProxyForwardsUserAgent(t *testing.T) {
	old := UserAgent
	UserAgent = "goophy/9.9.9"
	defer func() { UserAgent = old }()

	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer upstream.Close()

	p, err := NewOllamaProxy("127.0.0.1", "0", upstream.URL, "", UpstreamOllama)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p.handle(p.newReverseProxy()))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/tags", nil)
	req.Header.Set("User-Agent", "pi/1.2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "goophy/9.9.9 pi/1.2" {
		t.Fatalf("upstream saw User-Agent %q", got)
	}
}
