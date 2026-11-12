package stats

import (
	"fmt"
	"testing"
	"time"
)

// fakeClock returns a collector whose clock is advanced manually.
func fakeClock() (*Collector, *time.Time) {
	now := time.Unix(1_800_000_000, 0)
	c := New(Info{})
	c.clock = func() time.Time { return now }
	return c, &now
}

func TestNilCollectorAndTrackerAreNoOps(t *testing.T) {
	var c *Collector
	tr := c.Begin("GET", "/", "")
	if tr != nil {
		t.Fatalf("nil collector returned tracker %v", tr)
	}
	// None of these may panic.
	tr.SetModel("m")
	tr.SetStatus(200, true)
	tr.Wrote(10)
	tr.AddChunks(1)
	tr.SetUsage(1, 2, 0)
	tr.Finish("")
	c.SetUpstream("x")
	if _, err := fmt.Fprintln(c, "log"); err != nil {
		t.Fatal(err)
	}
	_ = c.Snapshot()
}

func TestRequestLifecycleAggregates(t *testing.T) {
	c, now := fakeClock()

	tr := c.Begin("POST", "/v1/chat/completions", "pi/1.0")
	tr.SetModel("coder")
	tr.SetStatus(200, true)
	*now = now.Add(500 * time.Millisecond)
	tr.Wrote(100)
	tr.AddChunks(10)

	s := c.Snapshot()
	if len(s.Active) != 1 || s.Active[0].CompletionTokens != 10 {
		t.Fatalf("active = %+v, want one request with 10 live tokens", s.Active)
	}

	*now = now.Add(2 * time.Second)
	tr.SetUsage(1000, 40, 600)
	tr.Finish("")

	s = c.Snapshot()
	if len(s.Active) != 0 || len(s.Recent) != 1 {
		t.Fatalf("active=%d recent=%d, want 0/1", len(s.Active), len(s.Recent))
	}
	r := s.Recent[0]
	if r.TTFB != 500*time.Millisecond || r.Duration != 2500*time.Millisecond {
		t.Errorf("TTFB=%v Duration=%v", r.TTFB, r.Duration)
	}
	if got := r.TokensPerSecond(); got != 20 {
		t.Errorf("TokensPerSecond = %v, want 20 (40 tokens over 2s)", got)
	}
	if s.Totals.PromptTokens != 1000 || s.Totals.CompletionTokens != 40 || s.Totals.Estimated {
		t.Errorf("totals = %+v", s.Totals)
	}
	if s.Totals.CachedTokens != 600 {
		t.Errorf("cached = %d, want 600", s.Totals.CachedTokens)
	}
	if len(s.Clients) != 1 || s.Clients[0].Client != "pi/1.0" || s.Clients[0].PromptTokens != 1000 || s.Clients[0].CachedTokens != 600 {
		t.Errorf("clients = %+v", s.Clients)
	}
	minPrompt := 0
	for _, mb := range s.Minutes {
		minPrompt += mb.PromptTokens
	}
	if len(s.Minutes) == 0 || minPrompt != 1000 {
		t.Errorf("minutes = %+v, want 1000 prompt tokens in total", s.Minutes)
	}
	if len(s.Models) != 1 || s.Models[0].Model != "coder" || s.Models[0].AvgTokensPerSecond() != 20 {
		t.Errorf("models = %+v", s.Models)
	}

	// Chunk counting (10) is backfilled to the reported 40 tokens over the
	// streaming window, so the series sums to the real total.
	sum := 0
	for _, b := range s.Series {
		sum += b.Chunks
	}
	if sum != 40 {
		t.Errorf("series tokens = %d, want 40", sum)
	}
}

func TestFinishWithoutUsageEstimatesFromChunks(t *testing.T) {
	c, _ := fakeClock()
	tr := c.Begin("POST", "/api/chat", "")
	tr.SetModel("m")
	tr.AddChunks(7)
	tr.Finish("")
	s := c.Snapshot()
	if s.Totals.CompletionTokens != 7 || !s.Totals.Estimated || !s.Models[0].Estimated {
		t.Errorf("totals=%+v models=%+v, want 7 estimated tokens", s.Totals, s.Models)
	}
}

func TestErrorsCounted(t *testing.T) {
	c, _ := fakeClock()
	a := c.Begin("POST", "/x", "")
	a.SetStatus(502, false)
	a.Finish("")
	b := c.Begin("POST", "/x", "")
	b.Finish("client disconnected")
	if got := c.Snapshot().Totals.Errors; got != 2 {
		t.Errorf("errors = %d, want 2", got)
	}
}

func TestFinishIsIdempotentAndFreezesRequest(t *testing.T) {
	c, _ := fakeClock()
	tr := c.Begin("GET", "/", "")
	tr.Finish("")
	tr.Finish("late")
	tr.SetModel("ignored")
	s := c.Snapshot()
	if s.Totals.Requests != 1 || s.Recent[0].Error != "" {
		t.Errorf("totals=%+v recent=%+v", s.Totals, s.Recent)
	}
}

func TestLogWriterSplitsLinesAndSpotsUpdates(t *testing.T) {
	c, _ := fakeClock()
	fmt.Fprint(c, "first line\nsecond ")
	fmt.Fprint(c, "half\n12:00:00 New version available: v9.9.9 (current: v1)\n")
	s := c.Snapshot()
	want := []string{"first line", "second half", "12:00:00 New version available: v9.9.9 (current: v1)"}
	if fmt.Sprint(s.Logs) != fmt.Sprint(want) {
		t.Errorf("logs = %q, want %q", s.Logs, want)
	}
	if s.UpdateNotice != "v9.9.9 (current: v1)" {
		t.Errorf("UpdateNotice = %q", s.UpdateNotice)
	}
}

func TestRecentIsBounded(t *testing.T) {
	c, _ := fakeClock()
	for range maxRecent + 10 {
		c.Begin("GET", "/", "").Finish("")
	}
	s := c.Snapshot()
	if len(s.Recent) != maxRecent || s.Recent[0].ID != maxRecent+10 {
		t.Errorf("len=%d newest=%d", len(s.Recent), s.Recent[0].ID)
	}
}
