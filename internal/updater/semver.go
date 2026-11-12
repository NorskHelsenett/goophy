package updater

import (
	"strconv"
	"strings"
)

// compareVersions compares two semantic versions ("1.2.3", "1.2.3-rc.1",
// an optional leading "v") as semver orders them: a prerelease sorts below
// its release, and numeric prerelease parts compare as numbers (rc.10 >
// rc.9). ok is false if either does not parse.
func compareVersions(a, b string) (cmp int, ok bool) {
	ac, ap, aok := parseVersion(a)
	bc, bp, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range ac {
		if ac[i] != bc[i] {
			if ac[i] < bc[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case ap == bp:
		return 0, true
	case ap == "":
		return 1, true
	case bp == "":
		return -1, true
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePart(as[i], bs[i]); c != 0 {
			return c, true
		}
	}
	switch {
	case len(as) < len(bs):
		return -1, true
	case len(as) > len(bs):
		return 1, true
	}
	return 0, true
}

func comparePart(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		if an < bn {
			return -1
		} else if an > bn {
			return 1
		}
		return 0
	case aerr == nil: // numeric parts sort below alphanumeric ones
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func parseVersion(v string) (core [3]uint64, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+") // build metadata does not order
	v, pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return core, "", false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return core, "", false
		}
		core[i] = n
	}
	return core, pre, true
}

// isPrerelease reports whether v is a prerelease such as 0.3.0-rc.1.
func isPrerelease(v string) bool {
	_, pre, ok := parseVersion(v)
	return ok && pre != ""
}
