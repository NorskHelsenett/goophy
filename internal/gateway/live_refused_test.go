package gateway

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

// TestRefusedChecksTheLoginFirst: a 401 only blames the login when the
// gateway confirms the token is no longer valid.
func TestRefusedChecksTheLoginFirst(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	var answer error
	checks := 0
	l := &Live{gateway: "https://gw.example", whoAmI: func(_ context.Context, c Credentials) (Me, error) {
		checks++
		if c.Token != "tok" || c.Gateway != "https://gw.example" {
			t.Errorf("checked %+v", c)
		}
		return Me{}, answer
	}}
	refuse := func(req string) string {
		buf.Reset()
		l.Refused("tok", req)
		l.pending.Wait()
		return buf.String()
	}

	// Valid login: the endpoint is named, the login isn't blamed.
	out := refuse("GET /api/models")
	if !strings.Contains(out, "GET /api/models although your goophy login is valid") || strings.Contains(out, "run 'goophy login'") {
		t.Errorf("valid login logged %q", out)
	}
	// Within a minute the confirmed login isn't checked again, and the same
	// request isn't logged twice.
	if out := refuse("GET /api/models"); out != "" || checks != 1 {
		t.Errorf("repeat logged %q after %d checks", out, checks)
	}

	// The gateway refuses the token itself: tell the user to log in.
	l.validTok = ""
	answer = &StatusError{Status: 401, Message: "not logged in"}
	if out := refuse("POST /v1/chat/completions"); !strings.Contains(out, "run 'goophy login'") {
		t.Errorf("refused login logged %q", out)
	}
	if out := refuse("POST /v1/chat/completions"); out != "" || checks != 2 {
		t.Errorf("refused token logged again (%q) or checked again (%d checks)", out, checks)
	}

	// The check itself fails: say so, without blaming the login.
	l2 := &Live{gateway: "https://gw.example", whoAmI: func(context.Context, Credentials) (Me, error) {
		return Me{}, errors.New("dial tcp: no route to host")
	}}
	buf.Reset()
	l2.Refused("tok", "GET /v1/models")
	l2.pending.Wait()
	if out := buf.String(); !strings.Contains(out, "checking the goophy login failed") || strings.Contains(out, "run 'goophy login'") {
		t.Errorf("failed check logged %q", out)
	}
}
