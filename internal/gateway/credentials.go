// Package gateway signs goophy in to the NHN LLM gateway and talks to the
// gateway's own API (who am I, usage, logout) with the token it got.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func CredentialsPath() (string, error) {
	dir := os.Getenv("GOOPHY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "goophy")
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// Load reads the stored credentials; ErrNotLoggedIn if there are none.
func Load() (Credentials, error) {
	var c Credentials
	p, err := CredentialsPath()
	if err != nil {
		return c, err
	}
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

// Expired reports whether the token has passed its expiry.
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
	return nil
}
