// Package stats collects in-memory, per-session request statistics and log
// lines for the interactive dashboard. Nothing is persisted: a Collector lives
// as long as the goophy process.
//
// All Collector and Tracker methods are safe for concurrent use, and are no-ops
// on a nil receiver so callers can track unconditionally.
package stats

import (
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// maxRecent bounds how many finished requests are kept for the Requests tab.
	maxRecent = 500
	// maxLogs bounds the log ring buffer.
	maxLogs = 2000
	// seriesLen is how many one-second buckets the time series keeps.
	seriesLen = 120
	// minutesLen is how many one-minute usage buckets are kept (24h).
	minutesLen = 24 * 60
)

// Request is one proxied request as seen by the dashboard.
type Request struct {
	ID           uint64
	Start        time.Time
	Method       string
	Path         string // client-facing path
	UpstreamPath string // path after routing (empty until forwarded)
	Model        string
	UserAgent    string
	Status       int
	Streaming    bool
	TTFB         time.Duration // time to first response byte (0 until received)
	Duration     time.Duration // total time (elapsed so far while active)
	Bytes        int64
	Chunks       int // streamed events/lines seen (a live proxy for tokens)

	// Token usage as reported by the upstream. HasUsage is false when the
	// response carried no usage block; CompletionTokens then falls back to Chunks.
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int // prompt tokens served from the upstream's prefix cache
	HasUsage         bool

	Note  string // e.g. "auto-pulled model"
	Error string
	Done  bool
}

// TokensPerSecond returns the generation speed: completion tokens over the time
// after the first byte. Zero when it can't be computed meaningfully.
func (r Request) TokensPerSecond() float64 {
	gen := r.Duration - r.TTFB
	if r.TTFB == 0 || gen < 50*time.Millisecond || r.CompletionTokens == 0 {
		return 0
	}
	return float64(r.CompletionTokens) / gen.Seconds()
}

// IsError reports whether the request failed (upstream error status or a
// transport/client error).
func (r Request) IsError() bool {
	return r.Error != "" || r.Status >= 400
}

// ModelStats aggregates finished requests per model.
type ModelStats struct {
	Model            string
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	Estimated        bool // some completion counts came from chunk counting
	TotalTTFB        time.Duration
	TTFBSamples      int
	GenTokens        int
	GenTime          time.Duration
	LastUsed         time.Time
}

// AvgTTFB returns the mean time to first byte.
func (m ModelStats) AvgTTFB() time.Duration {
	if m.TTFBSamples == 0 {
		return 0
	}
	return m.TotalTTFB / time.Duration(m.TTFBSamples)
}

// AvgTokensPerSecond returns the aggregate generation speed.
func (m ModelStats) AvgTokensPerSecond() float64 {
	if m.GenTime <= 0 {
		return 0
	}
	return float64(m.GenTokens) / m.GenTime.Seconds()
}

// ClientStats aggregates finished requests per client (User-Agent product).
type ClientStats struct {
	Client           string
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
}

// MinuteBucket is one minute of token usage.
type MinuteBucket struct {
	Unix             int64 // start of the minute
	Requests         int
	PromptTokens     int
	CompletionTokens int
}

// Bucket is one second of activity.
type Bucket struct {
	Unix     int64
	Requests int
	Chunks   int
	Errors   int
}

// Info describes the running proxy's configuration.
type Info struct {
	Version   string
	Listen    string
	Target    string
	APIKeySet bool
}

// Totals are session-wide counters.
type Totals struct {
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	Estimated        bool
	Bytes            int64
}

// Snapshot is a consistent copy of the collector state for rendering.
type Snapshot struct {
	Info         Info
	Started      time.Time
	Now          time.Time
	Upstream     string
	UpdateNotice string
	Totals       Totals
	Active       []Request // oldest first
	Recent       []Request // newest first, finished only
	Models       []ModelStats
	Clients      []ClientStats  // most tokens first
	Minutes      []MinuteBucket // oldest first, from the session start (max 24h) to Now
	Series       []Bucket       // oldest first, seriesLen entries ending at Now
	Logs         []string
}

// Collector accumulates statistics for one goophy session.
type Collector struct {
	mu           sync.Mutex
	info         Info
	started      time.Time
	upstream     string
	updateNotice string
	nextID       uint64
	active       map[uint64]*Request
	recent       []Request // ring, appended in finish order
	totals       Totals
	models       map[string]*ModelStats
	clients      map[string]*ClientStats
	series       [seriesLen]Bucket
	minutes      [minutesLen]MinuteBucket
	logs         []string
	logPartial   []byte
	clock        func() time.Time
}

// New returns an empty Collector.
func New(info Info) *Collector {
	return &Collector{
		info:     info,
		started:  time.Now(),
		upstream: "detecting…",
		active:   map[uint64]*Request{},
		models:   map[string]*ModelStats{},
		clients:  map[string]*ClientStats{},
		clock:    time.Now,
	}
}

// SetUpstream records the detected upstream flavor (or a status like "unreachable").
func (c *Collector) SetUpstream(s string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.upstream = s
	c.mu.Unlock()
}

// bucket returns the series bucket for t, resetting it if it holds an older second.
// Caller holds c.mu.
func (c *Collector) bucket(t time.Time) *Bucket {
	sec := t.Unix()
	b := &c.series[sec%seriesLen]
	if b.Unix != sec {
		*b = Bucket{Unix: sec}
	}
	return b
}

// minute returns the usage bucket for t's minute. Caller holds c.mu.
func (c *Collector) minute(t time.Time) *MinuteBucket {
	m := t.Unix() / 60
	b := &c.minutes[m%minutesLen]
	if b.Unix != m*60 {
		*b = MinuteBucket{Unix: m * 60}
	}
	return b
}

// Begin starts tracking a request and returns its Tracker.
func (c *Collector) Begin(method, path, userAgent string) *Tracker {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	now := c.clock()
	r := &Request{ID: c.nextID, Start: now, Method: method, Path: path, UserAgent: userAgent}
	c.active[r.ID] = r
	c.bucket(now).Requests++
	return &Tracker{c: c, r: r}
}

// Tracker updates one in-flight request.
type Tracker struct {
	c *Collector
	r *Request
}

func (t *Tracker) update(f func(r *Request)) {
	if t == nil {
		return
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if !t.r.Done {
		f(t.r)
	}
}

// SetModel records the requested model.
func (t *Tracker) SetModel(m string) { t.update(func(r *Request) { r.Model = m }) }

// SetUpstreamPath records the routed upstream path.
func (t *Tracker) SetUpstreamPath(p string) { t.update(func(r *Request) { r.UpstreamPath = p }) }

// SetNote attaches a short note (e.g. "auto-pulled model").
func (t *Tracker) SetNote(n string) { t.update(func(r *Request) { r.Note = n }) }

// SetStatus records the response status and whether the response is streamed.
func (t *Tracker) SetStatus(code int, streaming bool) {
	t.update(func(r *Request) { r.Status, r.Streaming = code, streaming })
}

// Wrote records n response bytes sent to the client.
func (t *Tracker) Wrote(n int) {
	t.update(func(r *Request) {
		if r.TTFB == 0 && n > 0 {
			r.TTFB = t.c.clock().Sub(r.Start)
		}
		r.Bytes += int64(n)
	})
}

// AddChunks records k streamed events.
func (t *Tracker) AddChunks(k int) {
	t.update(func(r *Request) {
		r.Chunks += k
		t.c.bucket(t.c.clock()).Chunks += k
	})
}

// SetUsage records upstream-reported token usage. cached is the number of
// prompt tokens served from the upstream's prefix cache (0 if not reported).
func (t *Tracker) SetUsage(prompt, completion, cached int) {
	t.update(func(r *Request) {
		r.PromptTokens, r.CompletionTokens, r.CachedTokens, r.HasUsage = prompt, completion, cached, true
	})
}

// Finish marks the request complete. errMsg is empty on success.
func (t *Tracker) Finish(errMsg string) {
	if t == nil {
		return
	}
	c := t.c
	c.mu.Lock()
	defer c.mu.Unlock()
	r := t.r
	if r.Done {
		return
	}
	now := c.clock()
	r.Done = true
	r.Duration = now.Sub(r.Start)
	r.Error = errMsg
	if !r.HasUsage && r.Chunks > 0 {
		r.CompletionTokens = r.Chunks
	}
	c.backfillTokens(r, now)
	delete(c.active, r.ID)

	c.recent = append(c.recent, *r)
	if len(c.recent) > maxRecent {
		c.recent = slices.Delete(c.recent, 0, len(c.recent)-maxRecent)
	}

	c.totals.Requests++
	c.totals.Bytes += r.Bytes
	c.totals.PromptTokens += r.PromptTokens
	c.totals.CompletionTokens += r.CompletionTokens
	c.totals.CachedTokens += r.CachedTokens
	mb := c.minute(now)
	mb.Requests++
	mb.PromptTokens += r.PromptTokens
	mb.CompletionTokens += r.CompletionTokens

	client := r.UserAgent
	if client == "" {
		client = "(unknown)"
	}
	cs := c.clients[client]
	if cs == nil {
		cs = &ClientStats{Client: client}
		c.clients[client] = cs
	}
	cs.Requests++
	cs.PromptTokens += r.PromptTokens
	cs.CompletionTokens += r.CompletionTokens
	cs.CachedTokens += r.CachedTokens
	if r.IsError() {
		cs.Errors++
	}

	estimated := !r.HasUsage && r.Chunks > 0
	c.totals.Estimated = c.totals.Estimated || estimated
	if r.IsError() {
		c.totals.Errors++
		c.bucket(now).Errors++
	}

	if r.Model == "" {
		return
	}
	m := c.models[r.Model]
	if m == nil {
		m = &ModelStats{Model: r.Model}
		c.models[r.Model] = m
	}
	m.Requests++
	m.LastUsed = now
	m.PromptTokens += r.PromptTokens
	m.CompletionTokens += r.CompletionTokens
	m.Estimated = m.Estimated || estimated
	if r.IsError() {
		m.Errors++
	}
	if r.TTFB > 0 {
		m.TotalTTFB += r.TTFB
		m.TTFBSamples++
	}
	if tps := r.TokensPerSecond(); tps > 0 {
		m.GenTokens += r.CompletionTokens
		m.GenTime += r.Duration - r.TTFB
	}
}

// backfillTokens corrects the throughput series once real usage is known.
// While streaming, the series counts chunks, but upstreams often pack several
// tokens into one chunk. The difference is spread evenly over the seconds the
// response was streaming, so the chart reflects actual tokens.
// Caller holds c.mu.
func (c *Collector) backfillTokens(r *Request, now time.Time) {
	extra := r.CompletionTokens - r.Chunks
	if !r.HasUsage || extra <= 0 || r.TTFB == 0 {
		return
	}
	first, last := r.Start.Add(r.TTFB).Unix(), now.Unix()
	first = max(first, last-seriesLen+1)
	n := int(last - first + 1)
	for sec := first; sec <= last; sec++ {
		share := extra / n
		if sec == last {
			share += extra % n
		}
		c.bucket(time.Unix(sec, 0)).Chunks += share
	}
}

// Write implements io.Writer so the collector can be the log output. Complete
// lines are appended to the log ring buffer.
func (c *Collector) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logPartial = append(c.logPartial, p...)
	for {
		i := slices.Index(c.logPartial, '\n')
		if i < 0 {
			break
		}
		line := string(c.logPartial[:i])
		c.logPartial = c.logPartial[i+1:]
		if strings.Contains(line, "New version available:") {
			if _, after, ok := strings.Cut(line, "New version available: "); ok {
				c.updateNotice = after
			}
		}
		c.logs = append(c.logs, line)
	}
	if len(c.logs) > maxLogs {
		c.logs = slices.Delete(c.logs, 0, len(c.logs)-maxLogs)
	}
	return len(p), nil
}

// Snapshot returns a copy of the current state.
func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	s := Snapshot{
		Info:         c.info,
		Started:      c.started,
		Now:          now,
		Upstream:     c.upstream,
		UpdateNotice: c.updateNotice,
		Totals:       c.totals,
		Logs:         slices.Clone(c.logs),
	}

	for _, r := range c.active {
		cp := *r
		cp.Duration = now.Sub(r.Start)
		if !cp.HasUsage {
			cp.CompletionTokens = cp.Chunks
		}
		s.Active = append(s.Active, cp)
	}
	slices.SortFunc(s.Active, func(a, b Request) int { return int(a.ID) - int(b.ID) })

	s.Recent = make([]Request, len(c.recent))
	for i, r := range c.recent {
		s.Recent[len(c.recent)-1-i] = r
	}

	for _, m := range c.models {
		s.Models = append(s.Models, *m)
	}
	slices.SortFunc(s.Models, func(a, b ModelStats) int { return b.LastUsed.Compare(a.LastUsed) })

	for _, cs := range c.clients {
		s.Clients = append(s.Clients, *cs)
	}
	slices.SortFunc(s.Clients, func(a, b ClientStats) int {
		return (b.PromptTokens + b.CompletionTokens) - (a.PromptTokens + a.CompletionTokens)
	})

	first, last := c.started.Unix()/60, now.Unix()/60
	first = max(first, last-minutesLen+1)
	for m := first; m <= last; m++ {
		b := c.minutes[m%minutesLen]
		if b.Unix != m*60 {
			b = MinuteBucket{Unix: m * 60}
		}
		s.Minutes = append(s.Minutes, b)
	}

	s.Series = make([]Bucket, seriesLen)
	nowSec := now.Unix()
	for i := range seriesLen {
		sec := nowSec - int64(seriesLen-1-i)
		b := c.series[((sec%seriesLen)+seriesLen)%seriesLen]
		if b.Unix != sec {
			b = Bucket{Unix: sec}
		}
		s.Series[i] = b
	}
	return s
}

// LiveTokensPerSecond estimates current output throughput from streamed chunks
// over the last window seconds (excluding the still-filling current second).
func (s Snapshot) LiveTokensPerSecond(window int) float64 {
	if window <= 0 || len(s.Series) < window+1 {
		return 0
	}
	n := 0
	for _, b := range s.Series[len(s.Series)-1-window : len(s.Series)-1] {
		n += b.Chunks
	}
	return float64(n) / float64(window)
}
