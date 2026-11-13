package gateway

import (
	"os"
	"strings"
	"time"
)

// localZone is the IANA name of the local time zone, so the gateway counts
// "today" in the user's zone; empty when it cannot be told.
func localZone() string {
	if tz := os.Getenv("TZ"); tz != "" && !strings.HasPrefix(tz, ":") {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			return name
		}
	}
	return ""
}
