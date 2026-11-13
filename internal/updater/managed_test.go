package updater

import "testing"

func TestPackageManagerFor(t *testing.T) {
	tests := map[string]string{
		"/opt/homebrew/Caskroom/goophy/0.3.0/goophy":              "Homebrew",
		"/usr/local/Caskroom/goophy/0.3.0/goophy":                 "Homebrew",
		"/home/linuxbrew/.linuxbrew/Caskroom/goophy/0.3.0/goophy": "Homebrew",
		"/opt/homebrew/Cellar/goophy/0.3.0/bin/goophy":            "Homebrew",
		"/Users/someone/.local/bin/goophy":                        "",
		"/usr/local/bin/goophy":                                   "",
		`C:\Users\someone\bin\goophy.exe`:                         "",
	}
	for exe, want := range tests {
		if got := packageManagerFor(exe); got != want {
			t.Errorf("packageManagerFor(%q) = %q, want %q", exe, got, want)
		}
	}
}
