// Package logsafe keeps client-supplied text from forging log lines or
// sending escape sequences to the operator's terminal.
//
// Much of what goophy logs (request paths, model names, upstream error
// bodies) comes from clients or upstreams. A newline in it would start a
// forged log line, and an ESC would reach the terminal (or the dashboard)
// as a control sequence.
package logsafe

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// String returns s with control characters (C0, DEL and C1, except tab)
// replaced by Go-style escapes such as \n and \x1b, so the result is one
// printable line. Invalid UTF-8 bytes are escaped as \xNN.
func String(s string) string {
	if clean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case isControl(r):
			if r < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// Truncate returns String(s), cut to at most max bytes of input with "…"
// appended when it was longer.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return String(s)
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return String(s[:cut]) + "…"
}

func clean(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || isControl(r) {
			return false
		}
		i += size
	}
	return true
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t') || (r >= 0x7f && r < 0xa0) ||
		// Bidi overrides and isolates can make a log line read differently
		// from what it contains.
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// Writer escapes control characters in what the log package writes to W.
// The log package writes each entry in a single Write ending in '\n'; that
// final newline is kept and every other control character is escaped, so
// one entry always stays one line.
type Writer struct{ W io.Writer }

// Write implements io.Writer. It reports len(p) on success, as the log
// package expects, even though the escaped output may be longer.
func (w Writer) Write(p []byte) (int, error) {
	s := string(p)
	nl := strings.HasSuffix(s, "\n")
	s = String(strings.TrimSuffix(s, "\n"))
	if nl {
		s += "\n"
	}
	if _, err := io.WriteString(w.W, s); err != nil {
		return 0, err
	}
	return len(p), nil
}
