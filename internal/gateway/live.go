package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// liveCheckInterval bounds how often Live looks at the credentials file.
const liveCheckInterval = time.Second

// Live hands a long-running 'goophy serve' the current login. It notices when
// 'goophy login' or 'goophy logout' in another shell changes the stored
// credentials, so serve follows along instead of proxying with a token that
// the new login has just revoked.
type Live struct {
	gateway string

	mu      sync.Mutex
	path    string
	checked time.Time
	file    os.FileInfo // the credentials file last read; nil if there was none
	cur     Credentials
	err     error
	refused string // the token the gateway last refused, so that is logged once

	// A 401 is checked against the gateway before blaming the login, because
	// some gateway endpoints refuse any goophy login.
	whoAmI   func(context.Context, Credentials) (Me, error)
	checking bool            // a check is running
	validTok string          // the token last confirmed valid...
	validAt  time.Time       // ...and when
	noted    map[string]bool // requests already logged as refused despite a valid login
	pending  sync.WaitGroup  // running checks, for tests
}

// validFor is how long a confirmed login is trusted before a 401 is checked again.
const validFor = time.Minute

// NewLive starts from the credentials serve was started with; serve keeps
// proxying to c.Gateway.
func NewLive(c Credentials) (*Live, error) {
	p, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	l := &Live{gateway: c.Gateway, path: p, cur: c, checked: time.Now(), whoAmI: WhoAmI}
	if fi, err := os.Stat(p); err == nil {
		l.file = fi
	}
	return l, nil
}

// Token returns the token to proxy with, or an error that tells the user what
// to do (log in again, or restart serve for another gateway).
func (l *Live) Token() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.checked) >= liveCheckInterval {
		l.checked = time.Now()
		l.reload()
	}
	if l.err != nil {
		return "", l.err
	}
	// No local expiry check: the gateway moves the expiry forward with each
	// use, so the expiresAt stored at login goes stale. The gateway decides,
	// and Refused explains its 401.
	return l.cur.Token, nil
}

// Refused is told when the gateway answered 401 to request ("GET /api/models")
// made with token. A 401 doesn't always mean the login is bad: some gateway
// endpoints refuse every goophy login. So Refused asks the gateway, in the
// background, whether the token is still valid, and logs either what to do
// about the login (once per token) or which endpoint refused a valid login
// (once per request).
func (l *Live) Refused(token, request string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if token == "" || token == l.refused || l.checking {
		return
	}
	if token == l.validTok && time.Since(l.validAt) < validFor {
		l.noteEndpoint(request)
		return
	}
	l.checking = true
	l.pending.Add(1)
	go l.verify(l.gateway, token, request)
}

// verify asks the gateway whether token is still valid after it refused request.
func (l *Live) verify(gw, token, request string) {
	defer l.pending.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := l.whoAmI(ctx, Credentials{Gateway: gw, Token: token})

	l.mu.Lock()
	defer l.mu.Unlock()
	l.checking = false
	var se *StatusError
	switch {
	case err == nil:
		l.validTok, l.validAt = token, time.Now()
		l.noteEndpoint(request)
	case errors.As(err, &se) && se.Status == 401:
		l.refused = token
		log.Print("The gateway refused your goophy login (expired after a week unused, or revoked); run 'goophy login'")
	default:
		log.Printf("The gateway answered 401 to %s, and checking the goophy login failed: %v", request, err)
	}
}

// noteEndpoint logs, once per request, that the gateway refused it although
// the login is valid. Callers hold l.mu.
func (l *Live) noteEndpoint(request string) {
	if l.noted[request] {
		return
	}
	if l.noted == nil {
		l.noted = map[string]bool{}
	}
	l.noted[request] = true
	log.Printf("The gateway answered 401 to %s although your goophy login is valid: that endpoint doesn't accept goophy logins", request)
}

// reload re-reads the credentials when the file changed. Save writes a new
// file and renames it into place, so a login always shows up as another file.
// Callers hold l.mu.
func (l *Live) reload() {
	fi, err := os.Stat(l.path)
	if err != nil {
		fi = nil
	}
	if unchanged(l.file, fi) {
		return
	}
	l.file = fi

	c, err := Load()
	switch {
	case errors.Is(err, ErrNotLoggedIn):
		if l.err == nil {
			log.Print("goophy login removed (goophy logout); requests are refused until you run 'goophy login'")
		}
		l.err = errors.New("goophy is logged out; run 'goophy login'")
	case err != nil:
		// Unreadable for now; keep what we have and look again.
		l.file = nil
	case c.Gateway != l.gateway:
		l.err = fmt.Errorf("goophy is now logged in to %s, but this serve proxies to %s; restart 'goophy serve'", c.Gateway, l.gateway)
		log.Print(l.err)
	default:
		if c.Token != l.cur.Token || l.err != nil {
			who := c.Email
			if who == "" {
				who = "your account"
			}
			log.Printf("Picked up the new goophy login for %s", who)
		}
		l.cur, l.err = c, nil
	}
}

func unchanged(old, cur os.FileInfo) bool {
	if old == nil || cur == nil {
		return old == nil && cur == nil
	}
	return os.SameFile(old, cur) && old.ModTime().Equal(cur.ModTime()) && old.Size() == cur.Size()
}
