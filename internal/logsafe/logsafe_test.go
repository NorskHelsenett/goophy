package logsafe

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	for in, want := range map[string]string{
		"plain /api/chat":    "plain /api/chat",
		"tab\tok":            "tab\tok",
		"GET /\nFORGED LINE": `GET /\nFORGED LINE`,
		"cr\rhere":           `cr\rhere`,
		"\x1b[2Jclear":       `\x1b[2Jclear`,
		"del\x7f":            `del\x7f`,
		"c1\u009bx":          `c1\x9bx`,
		"bidi\u202eevil":     `bidi\u202eevil`,
		"æøå 模型":             "æøå 模型",
		"bad\xffutf8":        `bad\xffutf8`,
	} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 3); got != "abc…" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("æøå", 3); got != "æ…" { // never splits a rune
		t.Errorf("got %q", got)
	}
	if got := Truncate("a\nb", 10); got != `a\nb` {
		t.Errorf("got %q", got)
	}
}

func TestWriterKeepsOneEntryOnOneLine(t *testing.T) {
	var buf bytes.Buffer
	l := log.New(Writer{&buf}, "", 0)
	l.Printf("401 to %s", "GET /\nFORGED LINE\x1b[31m")
	l.Print("second")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 || lines[0] != `401 to GET /\nFORGED LINE\x1b[31m` || lines[1] != "second" {
		t.Fatalf("got %q", buf.String())
	}
}
