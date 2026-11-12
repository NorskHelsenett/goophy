package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCredentialsLeaveRoamingProfile(t *testing.T) {
	local, roaming := t.TempDir(), t.TempDir()
	t.Setenv("GOOPHY_CONFIG_DIR", "")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)

	p, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(local, "goophy", "credentials.json"); p != want {
		t.Fatalf("CredentialsPath() = %s, want %s", p, want)
	}

	// A login stored by rc.1/rc.2 in the roaming profile moves over.
	old := filepath.Join(roaming, "goophy", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(`{"gateway":"https://gw.example","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || c.Token != "t" {
		t.Fatalf("Load() = %+v, %v", c, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("roaming copy still there: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("local copy missing: %v", err)
	}
}
