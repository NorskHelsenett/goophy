package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGateway hands out one code per approved challenge, like the gateway's
// /api/cli endpoints.
type fakeGateway struct {
	mu        sync.Mutex
	challenge string
	revoked   []string
}

func (f *fakeGateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/cli/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code, Verifier string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		sum := sha256.Sum256([]byte(in.Verifier))
		f.mu.Lock()
		ok := in.Code == "the-code" && base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"bad code"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"token":"gw_secret","tokenId":"t1","expiresAt":"2030-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("GET /api/cli/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gw_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"user":{"id":"u1","email":"me@example.org","name":"Me"}}`)
	})
	mux.HandleFunc("POST /api/cli/logout", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func TestLoginThroughBrowserCallback(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", ":0")
	f := &fakeGateway{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	// The "browser": approves on the gateway, which redirects to goophy's
	// listener with the code.
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("port") == "" || q.Get("challenge") == "" {
			t.Errorf("browser URL lacks port or challenge: %s", raw)
		}
		f.mu.Lock()
		f.challenge = q.Get("challenge")
		f.mu.Unlock()
		go func() {
			cb := "http://127.0.0.1:" + q.Get("port") + "/callback?code=the-code&state=" + url.QueryEscape(q.Get("state"))
			res, err := http.Get(cb)
			if err != nil {
				t.Errorf("callback: %v", err)
				return
			}
			res.Body.Close()
		}()
		return nil
	}
	pr, pw := io.Pipe() // stdin that never sends anything
	defer pw.Close()
	var out strings.Builder
	c, err := Login(context.Background(), LoginOptions{Gateway: srv.URL, In: pr, Out: &out, OpenBrowser: open})
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if c.Token != "gw_secret" || c.Email != "me@example.org" || c.Gateway != srv.URL || c.ExpiresAt == nil {
		t.Fatalf("credentials: %+v", c)
	}
	if !strings.Contains(out.String(), "/cli?") {
		t.Fatalf("no link printed for another browser:\n%s", out.String())
	}
}

func TestLoginWithPastedCode(t *testing.T) {
	f := &fakeGateway{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	// No browser: the user opens the printed link elsewhere and pastes the
	// code. The printed link must not carry a port, so the page shows the
	// code instead of redirecting to a listener on the wrong machine.
	pr, pw := io.Pipe()
	var out syncBuilder
	done := make(chan struct{})
	var c Credentials
	var err error
	go func() {
		c, err = Login(context.Background(), LoginOptions{Gateway: srv.URL, NoBrowser: true, In: pr, Out: &out})
		close(done)
	}()
	var link string
	for deadline := time.Now().Add(5 * time.Second); link == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, "/cli?") {
				link = strings.TrimSpace(line)
			}
		}
	}
	u, perr := url.Parse(link)
	if perr != nil || u.Query().Get("challenge") == "" {
		t.Fatalf("no link printed:\n%s", out.String())
	}
	if u.Query().Get("port") != "" {
		t.Fatalf("the manual link carries a port: %s", link)
	}
	f.mu.Lock()
	f.challenge = u.Query().Get("challenge")
	f.mu.Unlock()
	_, _ = io.WriteString(pw, "  the-code \n")
	<-done
	if err != nil || c.Token != "gw_secret" {
		t.Fatalf("login: %v %+v", err, c)
	}
}

type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestCallbackRejectsForeignState(t *testing.T) {
	codes := make(chan string, 1)
	errs := make(chan error, 1)
	h := callbackHandler("mine", codes, errs)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?code=x&state=theirs", nil))
	if rec.Code != http.StatusBadRequest || len(codes) != 0 {
		t.Fatalf("foreign state accepted: %d", rec.Code)
	}
}

func TestCredentialsRoundTripPrivately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOOPHY_CONFIG_DIR", filepath.Join(dir, "cfg"))
	if _, err := Load(); err != ErrNotLoggedIn {
		t.Fatalf("empty: %v", err)
	}
	if err := Save(Credentials{Gateway: "https://gw", Token: "gw_x"}); err != nil {
		t.Fatal(err)
	}
	p, _ := CredentialsPath()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode: %v %v", st.Mode(), err)
	}
	c, err := Load()
	if err != nil || c.Token != "gw_x" {
		t.Fatalf("load: %v %+v", err, c)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != ErrNotLoggedIn {
		t.Fatalf("after remove: %v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                           DefaultURL,
		"gw.chat.nhn.no":             "https://gw.chat.nhn.no",
		"https://gw.chat.nhn.no/v1/": "https://gw.chat.nhn.no",
		"http://localhost:8080/":     "http://localhost:8080",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestLiveFollowsLoginAndLogout(t *testing.T) {
	t.Setenv("GOOPHY_CONFIG_DIR", t.TempDir())
	gw := "https://gw.example"
	if err := Save(Credentials{Gateway: gw, Token: "old"}); err != nil {
		t.Fatal(err)
	}
	c, _ := Load()
	l, err := NewLive(c)
	if err != nil {
		t.Fatal(err)
	}
	token := func() (string, error) {
		l.mu.Lock()
		l.checked = time.Time{} // skip the once-a-second throttle
		l.mu.Unlock()
		return l.Token()
	}
	if tok, err := token(); tok != "old" || err != nil {
		t.Fatalf("Token() = %q, %v; want old", tok, err)
	}

	// goophy login in another shell.
	if err := Save(Credentials{Gateway: gw, Token: "new-token"}); err != nil {
		t.Fatal(err)
	}
	if tok, err := token(); tok != "new-token" || err != nil {
		t.Fatalf("after login, Token() = %q, %v; want new-token", tok, err)
	}

	// goophy logout.
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := token(); err == nil || !strings.Contains(err.Error(), "goophy login") {
		t.Fatalf("after logout, Token() err = %v; want a hint to log in", err)
	}

	// Logged in again.
	if err := Save(Credentials{Gateway: gw, Token: "third"}); err != nil {
		t.Fatal(err)
	}
	if tok, err := token(); tok != "third" || err != nil {
		t.Fatalf("after logging in again, Token() = %q, %v; want third", tok, err)
	}

	// Logged in to another gateway: serve cannot switch target.
	if err := Save(Credentials{Gateway: "https://other.example", Token: "other-gateway"}); err != nil {
		t.Fatal(err)
	}
	if _, err := token(); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("after login to another gateway, Token() err = %v; want restart hint", err)
	}

	// Expired.
	past := time.Now().Add(-time.Hour)
	if err := Save(Credentials{Gateway: gw, Token: "expired-token", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := token(); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired login: Token() err = %v; want expired", err)
	}
}
