package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UserAgent is sent with every request to the gateway.
var UserAgent = "goophy"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// NormalizeURL turns "gw.chat.nhn.no" or "https://gw.chat.nhn.no/v1/" into
// "https://gw.chat.nhn.no".
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("invalid gateway URL %q", raw)
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

// apiError is the gateway's error shape.
type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// StatusError is a non-2xx answer from the gateway.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("gateway: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("gateway: HTTP %d", e.Status)
}

// call sends a JSON request to base+path and decodes the answer into out
// (unless nil).
func call(ctx context.Context, method, base, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		var e apiError
		_ = json.Unmarshal(raw, &e)
		return &StatusError{Status: res.StatusCode, Message: e.Error.Message}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gateway: unexpected answer from %s: %w", path, err)
	}
	return nil
}

// Me is who a token belongs to.
type Me struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
	Token struct {
		ID        string     `json:"id"`
		ExpiresAt *time.Time `json:"expiresAt"`
	} `json:"token"`
}

// WhoAmI asks the gateway who the token belongs to.
func WhoAmI(ctx context.Context, c Credentials) (Me, error) {
	var me Me
	err := call(ctx, http.MethodGet, c.Gateway, "/api/cli/me", c.Token, nil, &me)
	return me, err
}

// Logout revokes the token on the gateway.
func Logout(ctx context.Context, c Credentials) error {
	return call(ctx, http.MethodPost, c.Gateway, "/api/cli/logout", c.Token, nil, nil)
}

// Totals are request and token counts.
type Totals struct {
	Requests         int64 `json:"requests"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	TotalTokens      int64 `json:"totalTokens"`
}

// UsageRow is one model's, team's or day's totals.
type UsageRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Totals
}

// Usage is the gateway's usage report.
type Usage struct {
	Totals  Totals     `json:"totals"`
	From    time.Time  `json:"from"`
	To      time.Time  `json:"to"`
	ByModel []UsageRow `json:"byModel"`
	ByTeam  []UsageRow `json:"byTeam"`
}

// GetUsage fetches the usage report for the last days, including today.
func GetUsage(ctx context.Context, c Credentials, days int) (Usage, error) {
	q := url.Values{}
	q.Set("days", fmt.Sprint(days))
	if tz := localZone(); tz != "" {
		q.Set("tz", tz)
	}
	var u Usage
	err := call(ctx, http.MethodGet, c.Gateway, "/api/cli/usage?"+q.Encode(), c.Token, nil, &u)
	return u, err
}
