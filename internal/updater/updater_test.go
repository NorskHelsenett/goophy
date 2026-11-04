package updater

import "testing"

func TestMatchAsset(t *testing.T) {
	// Asset names as published by v0.2.2, plus the legacy aliases.
	assets := []string{
		"goophy_0.2.3_checksums.txt",
		"goophy_Darwin_arm64.tar.gz",
		"goophy_Darwin_x86_64.tar.gz",
		"goophy_Linux_arm64.tar.gz",
		"goophy_Linux_x86_64.tar.gz",
		"goophy_Windows_arm64.zip",
		"goophy_Windows_x86_64.zip",
		"goophy_legacy_linux_amd64.tar.gz",
		"goophy_legacy_linux_arm64.tar.gz",
		"goophy_legacy_macos_amd64.tar.gz",
		"goophy_legacy_macos_arm64.tar.gz",
		"goophy_legacy_windows_amd64.zip",
		"goophy_legacy_windows_arm64.zip",
	}

	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "goophy_Darwin_arm64.tar.gz"},
		{"darwin", "amd64", "goophy_Darwin_x86_64.tar.gz"},
		{"linux", "arm64", "goophy_Linux_arm64.tar.gz"},
		{"linux", "amd64", "goophy_Linux_x86_64.tar.gz"},
		{"windows", "arm64", "goophy_Windows_arm64.zip"},
		{"windows", "amd64", "goophy_Windows_x86_64.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			i := matchAsset(assets, tt.goos, tt.goarch)
			if i < 0 {
				t.Fatalf("no match, want %s", tt.want)
			}
			if assets[i] != tt.want {
				t.Fatalf("got %s, want %s", assets[i], tt.want)
			}
		})
	}
}

func TestMatchAssetUnameOnly(t *testing.T) {
	// v0.2.2 published only uname-style names; the new matcher must still find them.
	assets := []string{
		"goophy_0.2.2_checksums.txt",
		"goophy_Darwin_arm64.tar.gz",
		"goophy_Linux_x86_64.tar.gz",
		"goophy_Windows_x86_64.zip",
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		if matchAsset(assets, p[0], p[1]) < 0 {
			t.Errorf("%s/%s: no match", p[0], p[1])
		}
	}
}

func TestMatchAssetNoMatch(t *testing.T) {
	assets := []string{"goophy_0.2.3_checksums.txt", "goophy_Linux_x86_64.tar.gz"}
	if i := matchAsset(assets, "freebsd", "amd64"); i != -1 {
		t.Fatalf("got %d, want -1", i)
	}
	if i := matchAsset(assets, "linux", "arm64"); i != -1 {
		t.Fatalf("got %d, want -1 (amd64 archive must not match arm64)", i)
	}
}
