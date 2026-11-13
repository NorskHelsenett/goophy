package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LoginOptions configure Login.
type LoginOptions struct {
	Gateway string // base URL, normalised
	// NoBrowser skips opening a browser: the user opens the printed link
	// anywhere and pastes the code it shows.
	NoBrowser bool
	In        io.Reader // where a pasted code is read from
	Out       io.Writer // where instructions are written
	// OpenBrowser opens a URL; nil uses the system's opener.
	OpenBrowser func(string) error
}

// Login signs in through the browser, much like OAuth's authorization code
// flow with PKCE: the gateway's /cli page asks the signed-in user to approve,
// and hands a one-time code to goophy's listener on 127.0.0.1, or shows it
// for the user to paste when the browser runs elsewhere. The code is then
// traded, with the verifier only goophy knows, for a personal token.
func Login(ctx context.Context, o LoginOptions) (Credentials, error) {
	verifier := randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(16)
	host, _ := os.Hostname()
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}

	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("state", state)
	q.Set("name", host)
	manualURL := o.Gateway + "/cli?" + q.Encode()

	codes := make(chan string, 2)
	errs := make(chan error, 2)

	// The listener catches the browser's redirect. It is best effort: when it
	// cannot listen, pasting the code still works.
	var browserURL string
	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		srv := &http.Server{Handler: callbackHandler(state, codes, errs), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
		q.Set("port", fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
		browserURL = o.Gateway + "/cli?" + q.Encode()
	}

	opened := false
	if !o.NoBrowser && browserURL != "" && canOpenBrowser() {
		open := o.OpenBrowser
		if open == nil {
			open = openBrowser
		}
		opened = open(browserURL) == nil
	}

	if opened {
		fmt.Fprintf(o.Out, "Opening your browser to sign in to %s...\n\n", o.Gateway)
		fmt.Fprintf(o.Out, "If it does not open, or you use a browser on another machine, visit:\n\n  %s\n\n", manualURL)
		fmt.Fprint(o.Out, "Paste code here if prompted > ")
	} else {
		fmt.Fprintf(o.Out, "Open this link in a browser to sign in to %s:\n\n  %s\n\n", o.Gateway, manualURL)
		fmt.Fprint(o.Out, "Paste the code it shows here > ")
	}

	// Either way a pasted code is accepted; whichever comes first wins.
	go func() {
		line, err := bufio.NewReader(o.In).ReadString('\n')
		if code := strings.TrimSpace(line); code != "" {
			codes <- code
			return
		}
		if err != nil && !opened {
			errs <- errors.New("no code was entered")
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var code string
	select {
	case code = <-codes:
	case err := <-errs:
		fmt.Fprintln(o.Out)
		return Credentials{}, err
	case <-ctx.Done():
		fmt.Fprintln(o.Out)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Credentials{}, errors.New("timed out waiting for the sign-in")
		}
		return Credentials{}, errors.New("cancelled")
	}
	fmt.Fprintln(o.Out)

	var tok struct {
		Token     string     `json:"token"`
		TokenID   string     `json:"tokenId"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if err := call(ctx, http.MethodPost, o.Gateway, "/api/cli/token", "",
		map[string]string{"code": code, "verifier": verifier}, &tok); err != nil {
		return Credentials{}, err
	}
	c := Credentials{Gateway: o.Gateway, Token: tok.Token, TokenID: tok.TokenID, ExpiresAt: tok.ExpiresAt}
	if me, err := WhoAmI(ctx, c); err == nil {
		c.Email, c.Name = me.User.Email, me.User.Name
	}
	return c, nil
}

// callbackHandler receives the browser's redirect from the gateway.
func callbackHandler(state string, codes chan<- string, errs chan<- error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "This sign-in link is not the one goophy is waiting for. Run goophy login again.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := q.Get("error"); e != "" {
			fmt.Fprint(w, page("Sign-in cancelled", "goophy was not signed in. You can close this tab."))
			errs <- fmt.Errorf("sign-in cancelled in the browser (%s)", e)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, page("goophy is signed in", "You can close this tab and go back to the terminal."))
		codes <- code
	})
	return mux
}

func page(title, text string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<title>` + html.EscapeString(title) + `</title>` +
		`<style>body{font-family:system-ui,sans-serif;display:grid;place-items:center;min-height:90vh;margin:0;color:#1a1a1a;background:#f6f6f4}` +
		`@media (prefers-color-scheme:dark){body{color:#eee;background:#16181a}}main{max-width:28rem;padding:1rem}h1{font-weight:400}</style>` +
		`</head><body><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(text) + `</p></main></body></html>`
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// canOpenBrowser reports whether a browser could show up for this user:
// not over SSH, and on Linux only with a display.
func canOpenBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	if runtime.GOOS == "linux" {
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return true
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
