package stats

import (
	"fmt"
	"strings"
	"testing"
)

func TestModelAndClientKeysAreBounded(t *testing.T) {
	c := New(Info{})
	for i := range maxKeys + 50 {
		tr := c.Begin("POST", "/api/chat", fmt.Sprintf("client-%d", i))
		tr.SetModel(fmt.Sprintf("model-%d", i))
		tr.Finish("")
	}
	s := c.Snapshot()
	if len(s.Models) != maxKeys+1 || len(s.Clients) != maxKeys+1 {
		t.Fatalf("models %d, clients %d; want %d each", len(s.Models), len(s.Clients), maxKeys+1)
	}
	for _, m := range s.Models {
		if m.Model == otherKey && m.Requests != 50 {
			t.Errorf("%s has %d requests, want 50", otherKey, m.Requests)
		}
	}
}

func TestCallerFieldsAreSanitised(t *testing.T) {
	c := New(Info{})
	tr := c.Begin("GET", "/\nFORGED\x1b[2J", "ua\x1b]0;title\x07")
	tr.SetModel("m\r\n" + strings.Repeat("x", 1000))
	tr.Finish("upstream said \x1b[31mno")
	fmt.Fprint(c, "log with \x1b[2J escape\n")

	s := c.Snapshot()
	r := s.Recent[len(s.Recent)-1]
	for _, v := range []string{r.Path, r.UserAgent, r.Model, r.Error, s.Logs[len(s.Logs)-1]} {
		if strings.ContainsAny(v, "\n\r\x1b\x07") {
			t.Errorf("control character kept in %q", v)
		}
	}
	if len(r.Model) > maxFieldLen+10 {
		t.Errorf("model kept at %d bytes", len(r.Model))
	}
}
