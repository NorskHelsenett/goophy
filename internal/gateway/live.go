package gateway

import (
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
}

// NewLive starts from the credentials serve was started with; serve keeps
// proxying to c.Gateway.
func NewLive(c Credentials) (*Live, error) {
	p, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	l := &Live{gateway: c.Gateway, path: p, cur: c, checked: time.Now()}
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
	if l.cur.Expired() {
		return "", errors.New("your goophy login has expired; run 'goophy login'")
	}
	return l.cur.Token, nil
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
