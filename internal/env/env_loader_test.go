package env

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// unset clears key for the test and restores it afterwards.
func unset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers restore
	os.Unsetenv(key)
}

func TestLastAssignmentInFileWins(t *testing.T) {
	unset(t, "GOOPHY_TEST_KEY")
	dir := t.TempDir()
	p := writeFile(t, dir, "a.env", "GOOPHY_TEST_KEY=old\n# GOOPHY_TEST_KEY=commented\nGOOPHY_TEST_KEY=\"new\"\n")

	if err := loadEnvFile(p, true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOOPHY_TEST_KEY"); got != "new" {
		t.Errorf("GOOPHY_TEST_KEY = %q, want the last value %q", got, "new")
	}
}

func TestOSEnvironmentBeatsFile(t *testing.T) {
	t.Setenv("GOOPHY_TEST_KEY", "from-shell")
	p := writeFile(t, t.TempDir(), "a.env", "GOOPHY_TEST_KEY=a\nGOOPHY_TEST_KEY=b\n")
	if err := loadEnvFile(p, true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOOPHY_TEST_KEY"); got != "from-shell" {
		t.Errorf("GOOPHY_TEST_KEY = %q, want the OS value", got)
	}
}

func TestEnvFileBeatsDotEnv(t *testing.T) {
	unset(t, "GOOPHY_TEST_KEY")
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, ".env", "GOOPHY_TEST_KEY=dotenv\n")
	custom := writeFile(t, dir, "custom.env", "GOOPHY_TEST_KEY=custom\n")

	if err := LoadEnv(custom); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOOPHY_TEST_KEY"); got != "custom" {
		t.Errorf("GOOPHY_TEST_KEY = %q, want --env-file to win over .env", got)
	}
}
