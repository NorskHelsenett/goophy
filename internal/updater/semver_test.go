package updater

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.2.4", "0.3.0-rc.1", -1},
		{"0.3.0-rc.1", "0.2.4", 1},
		{"0.3.0", "0.3.0-rc.1", 1},
		{"0.3.0-rc.2", "0.3.0-rc.10", -1},
		{"v0.3.0-rc.1", "0.3.0-rc.1", 0},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-rc.1", "1.0.0-beta", 1},
		{"0.10.0", "0.9.9", 1},
	} {
		got, ok := compareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, %v; want %d", c.a, c.b, got, ok, c.want)
		}
	}
	if _, ok := compareVersions("dev", "0.2.4"); ok {
		t.Error("dev parsed as a version")
	}
}

func TestPickRelease(t *testing.T) {
	tags := []string{"v0.2.4", "v0.3.0-rc.1", "v0.3.0-rc.2", "v0.2.3", "nightly"}
	if got := pickNewest(tags, true); got != "v0.3.0-rc.2" {
		t.Errorf("with prereleases: %q", got)
	}
	if got := pickNewest(tags, false); got != "v0.2.4" {
		t.Errorf("stable only: %q", got)
	}
}
