// Package gateway signs goophy in to the NHN LLM gateway and talks to the
// gateway's own API (who am I, usage, logout) with the token it got.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DefaultURL is the gateway goophy signs in to unless told otherwise.
const DefaultURL = "https://gw.chat.nhn.no"

// Credentials are what goophy keeps after a login.
type Credentials struct {
	Gateway   string     `json:"gateway"`
	Token     string     `json:"token"`
	TokenID   string     `json:"tokenId,omitempty"`
	Email     string     `json:"email,omitempty"`
	Name      string     `json:"name,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ErrNotLoggedIn means there are no stored credentials.
var ErrNotLoggedIn = errors.New("not logged in; run 'goophy login'")

// CredentialsPath is where the credentials are stored. GOOPHY_CONFIG_DIR
// overrides the directory (tests, several accounts).
//
// On Windows that is %LocalAppData%, not the %AppData% os.UserConfigDir
// gives: %AppData% is the roaming profile, which domains copy to every PC the
// user signs in to and often redirect to a file share. The token must stay on
// the machine it was issued to.
func CredentialsPath() (string, error) {
	dir := os.Getenv("GOOPHY_CONFIG_DIR")
	if dir == "" {
		base, err := configBase()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "goophy")
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func configBase() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return "", errors.New("%LocalAppData% is not set")
	}
	return os.UserConfigDir()
}

// legacyPath is where goophy 0.3.0-rc.1 and rc.2 stored the credentials on
// Windows (the roaming %AppData%); "" elsewhere.
func legacyPath() string {
	if runtime.GOOS != "windows" || os.Getenv("GOOPHY_CONFIG_DIR") != "" {
		return ""
	}
	d := os.Getenv("APPDATA")
	if d == "" {
		return ""
	}
	return filepath.Join(d, "goophy", "credentials.json")
}

// migrateLegacy moves credentials from the roaming profile to the local one,
// so the token stops travelling with the profile. Best effort.
func migrateLegacy(to string) {
	from := legacyPath()
	if from == "" || from == to {
		return
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return
	}
	var c Credentials
	if json.Unmarshal(b, &c) == nil && c.Token != "" {
		if _, err := os.Stat(to); errors.Is(err, os.ErrNotExist) {
			if Save(c) != nil {
				return
			}
		}
	}
	_ = os.Remove(from)
	_ = os.Remove(filepath.Dir(from)) // only if empty
}

// Load reads the stored credentials; ErrNotLoggedIn if there are none.
func Load() (Credentials, error) {
	var c Credentials
	p, err := CredentialsPath()
	if err != nil {
		return c, err
	}
	migrateLegacy(p)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotLoggedIn
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("read %s: %w", p, err)
	}
	if c.Token == "" || c.Gateway == "" {
		return c, ErrNotLoggedIn
	}
	return c, nil
}

// Expired reports whether the token has passed the expiry stored with it.
// The gateway extends a token's expiry as it is used, so a stored expiry in
// the past means "ask the gateway", not "expired".
func (c Credentials) Expired() bool {
	return c.ExpiresAt != nil && time.Now().After(*c.ExpiresAt)
}

// Save stores credentials readable by the user only: the token acts as
// them.
func Save(c Credentials) error {
	p, err := CredentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600 whatever is lying around, and the
	// rename replaces the old file, with its mode, in one step.
	f, err := os.CreateTemp(dir, "credentials-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// Remove deletes the stored credentials; it is not an error if there are
// none.
func Remove() error {
	p, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if l := legacyPath(); l != "" {
		_ = os.Remove(l)
	}
	return nil
}
