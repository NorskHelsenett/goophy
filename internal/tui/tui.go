// Package tui renders goophy's interactive terminal dashboard: live session
// statistics, a request inspector, per-model usage and the log stream, driven
// by a stats.Collector.
package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

// refreshInterval is how often the dashboard re-reads the collector.
const refreshInterval = 250 * time.Millisecond

type tab int

const (
	tabDashboard tab = iota
	tabUsage
	tabRequests
	tabModels
	tabLogs
)

var tabNames = []string{"Dashboard", "Usage", "Requests", "Models", "Logs"}

// StoppedMsg tells the dashboard the server has stopped; it quits on receipt.
type StoppedMsg struct{ Err error }

type tickMsg time.Time

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	c    *stats.Collector
	snap stats.Snapshot
	tab  tab
	w, h int

	// Requests tab: selection is by request ID so it stays put as new
	// requests arrive. selID 0 follows the newest request.
	selID  uint64
	detail bool

	// Logs tab: lines scrolled up from the bottom; 0 follows the tail.
	logScroll int
	logFilter string
	filtering bool
}

// New returns a dashboard model reading from c.
func New(c *stats.Collector) Model {
	return Model{c: c, snap: c.Snapshot(), w: 100, h: 30, detail: true}
}

// NewProgram returns a full-screen dashboard program for c. Run it to block
// until the user quits; send StoppedMsg to close it from elsewhere.
func NewProgram(c *stats.Collector) *tea.Program {
	return tea.NewProgram(New(c), tea.WithAltScreen())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		m.snap = m.c.Snapshot()
		return m, tick()
	case StoppedMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch k.Type {
		case tea.KeyEnter, tea.KeyEsc:
			m.filtering = false
			if k.Type == tea.KeyEsc {
				m.logFilter = ""
			}
		case tea.KeyBackspace:
			if r := []rune(m.logFilter); len(r) > 0 {
				m.logFilter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.logFilter += string(k.Runes)
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
		m.logScroll = 0
		return m, nil
	}

	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % tab(len(tabNames))
	case "shift+tab", "left", "h":
		m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
	case "1", "2", "3", "4", "5":
		m.tab = tab(k.String()[0] - '1')
	}

	switch m.tab {
	case tabRequests:
		list := m.requestList()
		switch k.String() {
		case "up", "k":
			m.selID = moveSel(list, m.selID, -1)
		case "down", "j":
			m.selID = moveSel(list, m.selID, 1)
		case "home", "g":
			m.selID = 0
		case "end", "G":
			if len(list) > 0 {
				m.selID = list[len(list)-1].ID
			}
		case "enter", " ":
			m.detail = !m.detail
		}
	case tabLogs:
		page := max(1, m.bodyHeight()-2)
		switch k.String() {
		case "up", "k":
			m.logScroll++
		case "down", "j":
			m.logScroll--
		case "pgup", "ctrl+u":
			m.logScroll += page
		case "pgdown", "ctrl+d":
			m.logScroll -= page
		case "home", "g":
			m.logScroll = len(m.snap.Logs)
		case "end", "G":
			m.logScroll = 0
		case "/":
			m.filtering = true
		case "esc":
			m.logFilter = ""
		}
		m.logScroll = max(0, m.logScroll)
	}
	return m, nil
}

// requestList is active requests (newest first) followed by finished ones.
func (m Model) requestList() []stats.Request {
	list := make([]stats.Request, 0, len(m.snap.Active)+len(m.snap.Recent))
	for i := len(m.snap.Active) - 1; i >= 0; i-- {
		list = append(list, m.snap.Active[i])
	}
	return append(list, m.snap.Recent...)
}

func indexOf(list []stats.Request, id uint64) int {
	for i, r := range list {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func moveSel(list []stats.Request, id uint64, d int) uint64 {
	if len(list) == 0 {
		return 0
	}
	i := indexOf(list, id)
	if i < 0 {
		i = 0
	}
	i = min(max(i+d, 0), len(list)-1)
	if i == 0 {
		return 0 // back at the top: follow newest again
	}
	return list[i].ID
}

// bodyHeight is the space left for the active tab under the header and tab bar
// and above the footer.
func (m Model) bodyHeight() int { return max(1, m.h-3) }

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(ansi.Truncate(m.header(), m.w, "…"))
	b.WriteByte('\n')
	b.WriteString(ansi.Truncate(m.tabBar(), m.w, "…"))
	b.WriteByte('\n')

	var body string
	switch m.tab {
	case tabDashboard:
		body = m.dashboard()
	case tabUsage:
		body = m.usage()
	case tabRequests:
		body = m.requests()
	case tabModels:
		body = m.models()
	case tabLogs:
		body = m.logs()
	}
	b.WriteString(fitHeight(body, m.bodyHeight(), m.w))
	b.WriteByte('\n')
	b.WriteString(ansi.Truncate(m.footer(), m.w, "…"))
	return b.String()
}

// fitHeight pads or cuts s to exactly h lines, each truncated to width w.
func fitHeight(s string, h, w int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m Model) header() string {
	s := m.snap
	left := sBrand.Render(" goophy ") + " " + sDim.Render(s.Info.Version) + " " + sWarn.Render("preview")

	dot, upStyle := "●", sOK
	switch {
	case strings.HasPrefix(s.Upstream, "unreachable"):
		upStyle = sErr
	case s.Upstream == "unknown" || strings.HasPrefix(s.Upstream, "detecting"):
		upStyle = sWarn
	}
	left += "  " + upStyle.Render(dot+" "+s.Upstream)
	left += "  " + sDim.Render(s.Info.Listen+" → "+s.Info.Target)
	if !s.Info.APIKeySet {
		left += sDim.Render("  (no API key)")
	}

	right := ""
	if s.UpdateNotice != "" {
		right = sWarn.Render("⬆ update " + s.UpdateNotice + " — run 'goophy update'")
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if right == "" || gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) tabBar() string {
	parts := make([]string, len(tabNames))
	for i, n := range tabNames {
		label := fmt.Sprintf(" %d %s ", i+1, n)
		if i == int(m.tab) && n == "Requests" && len(m.snap.Active) > 0 {
			label = fmt.Sprintf(" %d %s (%d) ", i+1, n, len(m.snap.Active))
		}
		if tab(i) == m.tab {
			parts[i] = sTabActive.Render(label)
		} else {
			parts[i] = sTab.Render(label)
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) footer() string {
	keys := "1-5/tab switch · q quit"
	switch m.tab {
	case tabRequests:
		keys = "↑↓ select · enter details · g newest · " + keys
	case tabLogs:
		if m.filtering {
			return sTabActive.Render(" filter: ") + " " + m.logFilter + "█  " + sDim.Render("enter apply · esc clear")
		}
		keys = "↑↓/pgup/pgdn scroll · G follow · / filter · " + keys
		if m.logFilter != "" {
			keys = "filter " + fmt.Sprintf("%q", m.logFilter) + " (esc clears) · " + keys
		}
	}
	return sDim.Render(" " + keys)
}

// ---------------------------------------------------------------- dashboard

func (m Model) dashboard() string {
	s := m.snap
	t := s.Totals

	errRate := ""
	if t.Requests > 0 {
		errRate = fmt.Sprintf("%.1f%%", 100*float64(t.Errors)/float64(t.Requests))
	}
	outSuffix := ""
	if t.Estimated {
		outSuffix = "~"
	}
	errStyle := sValue
	if t.Errors > 0 {
		errStyle = sErrValue
	}

	tiles := []string{
		tile("Requests", sValue.Render(humanInt(t.Requests+len(s.Active))), fmt.Sprintf("%d in flight", len(s.Active))),
		tile("Errors", errStyle.Render(humanInt(t.Errors)), errRate),
		tile("Tokens in", sValue.Render(humanInt(t.PromptTokens)), "prompt"),
		tile("Tokens out", sValue.Render(outSuffix+humanInt(t.CompletionTokens)), "completion"),
		tile("Throughput", sValue.Render(fmt.Sprintf("%.1f", s.LiveTokensPerSecond(5))), "tok/s (5s)"),
		tile("Uptime", sValue.Render(humanDur(s.Now.Sub(s.Started))), humanBytes(t.Bytes)+" out"),
	}
	var rows []string
	perRow := max(1, min(len(tiles), m.w/lipgloss.Width(tiles[0])))
	for i := 0; i < len(tiles); i += perRow {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, tiles[i:min(i+perRow, len(tiles))]...))
	}

	var b strings.Builder
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\n")

	// Charts over the most recent seconds that fit the width.
	chartW := max(10, min(len(s.Series)-1, m.w-22))
	series := s.Series[len(s.Series)-1-chartW : len(s.Series)-1]
	tok := make([]int, len(series))
	req := make([]int, len(series))
	errs := make([]int, len(series))
	for i, bk := range series {
		tok[i], req[i], errs[i] = bk.Chunks, bk.Requests, bk.Errors
	}
	fmt.Fprintf(&b, "%s %s %s\n", sLabel.Render(pad("Tokens/s", 12)), sChartTok.Render(sparkline(tok)), sDim.Render("peak "+humanInt(maxInt(tok))))
	fmt.Fprintf(&b, "%s %s %s\n", sLabel.Render(pad("Requests/s", 12)), sChartReq.Render(sparkline(req)), sDim.Render("peak "+humanInt(maxInt(req))))
	if maxInt(errs) > 0 {
		fmt.Fprintf(&b, "%s %s\n", sLabel.Render(pad("Errors/s", 12)), sErr.Render(sparkline(errs)))
	}
	fmt.Fprintf(&b, "%s %s\n\n", strings.Repeat(" ", 12), sDim.Render(pad(fmt.Sprintf("-%ds", chartW), chartW-3)+"now"))

	b.WriteString(sSection.Render("In flight"))
	b.WriteByte('\n')
	if len(s.Active) == 0 {
		b.WriteString(sDim.Render("  idle — waiting for requests"))
		b.WriteByte('\n')
	} else {
		for i := len(s.Active) - 1; i >= 0; i-- {
			r := s.Active[i]
			live := ""
			if r.TTFB > 0 && r.Duration > r.TTFB && r.Chunks > 0 {
				live = fmt.Sprintf("%.1f tok/s", float64(r.Chunks)/(r.Duration-r.TTFB).Seconds())
			}
			state := sWarn.Render("waiting")
			if r.TTFB > 0 {
				state = sOK.Render("streaming")
			}
			fmt.Fprintf(&b, "  %s %s %s %s %s %s\n",
				state, pad(orDash(r.Model), 22), pad(r.Path, 22),
				pad(humanDur(r.Duration), 7), pad(humanInt(r.Chunks)+" chunks", 12), sDim.Render(live))
		}
	}

	b.WriteByte('\n')
	b.WriteString(sSection.Render("Recent"))
	b.WriteByte('\n')
	if len(s.Recent) == 0 {
		b.WriteString(sDim.Render("  nothing yet"))
	}
	for _, r := range s.Recent[:min(len(s.Recent), 8)] {
		b.WriteString("  " + m.requestRow(r, false))
		b.WriteByte('\n')
	}
	return b.String()
}

func tile(label, value, sub string) string {
	return sTile.Render(sLabel.Render(label) + "\n" + value + "\n" + sDim.Render(sub))
}

// ---------------------------------------------------------------- usage

func (m Model) usage() string {
	s := m.snap
	t := s.Totals
	var b strings.Builder

	est := ""
	if t.Estimated {
		est = "~"
	}
	b.WriteString(sSection.Render("Session tokens"))
	b.WriteByte('\n')
	kv := func(k, v string) string { return sLabel.Render(k) + " " + sValue.Render(v) + "   " }
	line := kv("total", est+humanInt(t.PromptTokens+t.CompletionTokens)) +
		kv("prompt", humanInt(t.PromptTokens)) +
		kv("completion", est+humanInt(t.CompletionTokens))
	if t.CachedTokens > 0 && t.PromptTokens > 0 {
		line += kv("cached", fmt.Sprintf("%s (%.0f%% of prompt)", humanInt(t.CachedTokens), 100*float64(t.CachedTokens)/float64(t.PromptTokens)))
	}
	b.WriteString("  " + line + "\n")
	if t.Requests > 0 {
		b.WriteString("  " + sDim.Render(fmt.Sprintf("per request: %s prompt · %s completion · over %d requests",
			humanInt(t.PromptTokens/t.Requests), humanInt(t.CompletionTokens/t.Requests), t.Requests)) + "\n")
	}
	b.WriteByte('\n')

	// Tokens per minute across the session.
	mins := s.Minutes
	chartW := max(5, m.w-10)
	if len(mins) > chartW {
		mins = mins[len(mins)-chartW:]
	}
	total := make([]int, len(mins))
	comp := make([]int, len(mins))
	for i, mb := range mins {
		total[i], comp[i] = mb.PromptTokens+mb.CompletionTokens, mb.CompletionTokens
	}
	b.WriteString(sSection.Render("Tokens per minute") + sDim.Render(fmt.Sprintf("  (prompt + completion, last %dm)", len(mins))))
	b.WriteByte('\n')
	peak := maxInt(total)
	for i, row := range barChart(total, 5) {
		label := "      "
		switch i {
		case 0:
			label = fmt.Sprintf("%6s", humanInt(peak))
		case 4:
			label = fmt.Sprintf("%6s", "0")
		}
		b.WriteString(sDim.Render(label) + " " + sChartTok.Render(row) + "\n")
	}
	if len(mins) > 0 {
		start := time.Unix(mins[0].Unix, 0).Format("15:04")
		b.WriteString(sDim.Render("       "+pad(start, max(0, len(mins)-3))+"now") + "\n")
	}
	b.WriteString(sDim.Render(pad("output", 6)) + " " + sChartReq.Render(sparkline(comp)) + sDim.Render("  peak "+humanInt(maxInt(comp))+"/min") + "\n\n")

	// By client.
	b.WriteString(sSection.Render("By client"))
	b.WriteByte('\n')
	if len(s.Clients) == 0 {
		b.WriteString(sDim.Render("  no finished requests yet") + "\n")
	} else {
		b.WriteString(sLabel.Render(fmt.Sprintf("  %-20s %6s %6s %9s %9s %10s  %s", "client", "reqs", "errors", "prompt", "cached", "completion", "share")) + "\n")
		sum := t.PromptTokens + t.CompletionTokens
		barW := max(5, m.w-80)
		for _, c := range s.Clients {
			tok := c.PromptTokens + c.CompletionTokens
			frac := 0.0
			if sum > 0 {
				frac = float64(tok) / float64(sum)
			}
			n := int(frac * float64(barW))
			cached := "-"
			if c.CachedTokens > 0 {
				cached = humanInt(c.CachedTokens)
			}
			fmt.Fprintf(&b, "  %s %6s %6s %9s %9s %10s  %s %s\n",
				pad(c.Client, 20), humanInt(c.Requests), humanInt(c.Errors),
				humanInt(c.PromptTokens), cached, humanInt(c.CompletionTokens),
				sChartTok.Render(strings.Repeat("█", n))+sRule.Render(strings.Repeat("░", barW-n)),
				sDim.Render(fmt.Sprintf("%.0f%%", frac*100)))
		}
	}
	b.WriteByte('\n')

	// Largest prompts: where context goes.
	big := slices.Clone(s.Recent)
	big = slices.DeleteFunc(big, func(r stats.Request) bool { return !r.HasUsage })
	slices.SortFunc(big, func(a, b stats.Request) int { return b.PromptTokens - a.PromptTokens })
	b.WriteString(sSection.Render("Largest prompts"))
	b.WriteByte('\n')
	if len(big) == 0 {
		b.WriteString(sDim.Render("  no reported usage yet") + "\n")
	}
	for _, r := range big[:min(5, len(big))] {
		fmt.Fprintf(&b, "  %s %s %s %s %s\n",
			r.Start.Format("15:04:05"), pad(orDash(r.Model), 22), pad(orDash(r.UserAgent), 16),
			sValue.Render(pad(humanInt(r.PromptTokens)+" in", 10)),
			sDim.Render(humanInt(r.CompletionTokens)+" out"))
	}
	return b.String()
}

// barChart renders vals as a vertical bar chart height rows tall, using
// eighth-block characters for sub-row precision. Rows are returned top first.
func barChart(vals []int, height int) []string {
	top := maxInt(vals)
	rows := make([][]rune, height)
	for i := range rows {
		rows[i] = []rune(strings.Repeat(" ", len(vals)))
	}
	for x, v := range vals {
		if top == 0 || v == 0 {
			rows[height-1][x] = '·'
			continue
		}
		eighths := max(1, v*height*8/top) // any non-zero value is visible
		for y := height - 1; y >= 0 && eighths > 0; y-- {
			n := min(8, eighths)
			rows[y][x] = sparkRunes[n]
			eighths -= n
		}
	}
	out := make([]string, height)
	for i, r := range rows {
		out[i] = string(r)
	}
	return out
}

// ---------------------------------------------------------------- requests

var reqCols = []struct {
	name string
	w    int
}{{"time", 8}, {"status", 6}, {"method", 6}, {"path", 24}, {"model", 22}, {"ttfb", 7}, {"dur", 7}, {"in", 6}, {"out", 7}, {"tok/s", 6}}

func (m Model) requestRow(r stats.Request, selected bool) string {
	status := statusText(r)
	out := "-"
	switch {
	case r.HasUsage:
		out = humanInt(r.CompletionTokens)
	case r.CompletionTokens > 0:
		out = "~" + humanInt(r.CompletionTokens)
	}
	in := "-"
	if r.HasUsage {
		in = humanInt(r.PromptTokens)
	}
	tps := "-"
	if v := r.TokensPerSecond(); v > 0 {
		tps = fmt.Sprintf("%.1f", v)
	}
	cells := []string{
		r.Start.Format("15:04:05"), status, r.Method, r.Path, orDash(r.Model),
		durOrDash(r.TTFB), humanDur(r.Duration), in, out, tps,
	}
	var b strings.Builder
	for i, c := range cells {
		cell := pad(c, reqCols[i].w)
		if i == 1 && !selected {
			cell = statusStyle(r).Render(cell)
		}
		b.WriteString(cell + " ")
	}
	line := b.String()
	if selected {
		return sSelected.Render(ansi.Truncate(line, m.w-1, "…"))
	}
	return line
}

func (m Model) requests() string {
	list := m.requestList()
	if len(list) == 0 {
		return sDim.Render("\n  No requests yet. Point a client at " + m.snap.Info.Listen + ".")
	}
	sel := max(0, indexOf(list, m.selID))

	detailH := 0
	if m.detail {
		detailH = 9
	}
	rowsH := max(3, m.bodyHeight()-1-detailH)

	var hdr strings.Builder
	for _, c := range reqCols {
		hdr.WriteString(pad(c.name, c.w) + " ")
	}
	var b strings.Builder
	b.WriteString(sLabel.Render(hdr.String()))
	b.WriteByte('\n')

	// Scroll so the selection stays visible.
	start := 0
	if sel >= rowsH {
		start = sel - rowsH + 1
	}
	for i := start; i < min(len(list), start+rowsH); i++ {
		b.WriteString(m.requestRow(list[i], i == sel))
		b.WriteByte('\n')
	}
	for i := min(len(list), start+rowsH) - start; i < rowsH; i++ {
		b.WriteByte('\n')
	}
	if m.detail {
		b.WriteString(m.requestDetail(list[sel]))
	}
	return b.String()
}

func (m Model) requestDetail(r stats.Request) string {
	kv := func(k, v string) string { return sLabel.Render(pad(k, 10)) + v }
	upstream := r.UpstreamPath
	if upstream == "" {
		upstream = "-"
	} else if upstream != r.Path {
		upstream = r.Path + " → " + upstream
	}
	usage := "not reported by upstream"
	if r.HasUsage {
		usage = fmt.Sprintf("%s prompt + %s completion", humanInt(r.PromptTokens), humanInt(r.CompletionTokens))
	} else if r.Chunks > 0 {
		usage += fmt.Sprintf(" (~%s from %d chunks)", humanInt(r.Chunks), r.Chunks)
	}
	state := "done"
	if !r.Done {
		state = "in flight"
	}
	if r.Error != "" {
		state = sErr.Render(r.Error)
	}
	if r.Note != "" {
		state += " · " + r.Note
	}
	mode := "buffered"
	if r.Streaming {
		mode = "streamed"
	}
	lines := []string{
		sSection.Render(fmt.Sprintf("Request #%d", r.ID)),
		kv("route", r.Method+" "+upstream),
		kv("model", orDash(r.Model)),
		kv("status", statusStyle(r).Render(statusText(r))+"  "+state),
		kv("timing", fmt.Sprintf("first byte %s · total %s · %s", durOrDash(r.TTFB), humanDur(r.Duration), mode)),
		kv("tokens", usage),
		kv("size", fmt.Sprintf("%s · %d chunks", humanBytes(r.Bytes), r.Chunks)),
		kv("client", orDash(r.UserAgent)),
	}
	return sRule.Render(strings.Repeat("─", max(0, m.w))) + "\n" + strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- models

func (m Model) models() string {
	if len(m.snap.Models) == 0 {
		return sDim.Render("\n  No model usage yet this session.")
	}
	cols := []struct {
		n string
		w int
	}{{"model", 28}, {"reqs", 6}, {"errors", 6}, {"tokens in", 10}, {"tokens out", 10}, {"avg ttfb", 9}, {"avg tok/s", 9}, {"last used", 10}}
	var b strings.Builder
	for _, c := range cols {
		b.WriteString(pad(c.n, c.w) + " ")
	}
	out := sLabel.Render(b.String()) + "\n"

	// Active request counts per model.
	busy := map[string]int{}
	for _, r := range m.snap.Active {
		busy[r.Model]++
	}

	for _, ms := range m.snap.Models {
		tokOut := humanInt(ms.CompletionTokens)
		if ms.Estimated {
			tokOut = "~" + tokOut
		}
		tps := "-"
		if v := ms.AvgTokensPerSecond(); v > 0 {
			tps = fmt.Sprintf("%.1f", v)
		}
		name := ms.Model
		if n := busy[ms.Model]; n > 0 {
			name = fmt.Sprintf("%s (%d active)", name, n)
		}
		errs := pad(humanInt(ms.Errors), 6)
		if ms.Errors > 0 {
			errs = sErr.Render(errs)
		}
		cells := []string{
			pad(name, 28), pad(humanInt(ms.Requests), 6), errs,
			pad(humanInt(ms.PromptTokens), 10), pad(tokOut, 10),
			pad(durOrDash(ms.AvgTTFB()), 9), pad(tps, 9),
			pad(humanDur(m.snap.Now.Sub(ms.LastUsed))+" ago", 10),
		}
		out += strings.Join(cells, " ") + "\n"
	}

	// Share of output tokens per model as a simple bar chart.
	total := 0
	for _, ms := range m.snap.Models {
		total += ms.CompletionTokens
	}
	if total > 0 && len(m.snap.Models) > 1 {
		out += "\n" + sSection.Render("Share of output tokens") + "\n"
		barW := max(10, m.w-40)
		for _, ms := range m.snap.Models {
			frac := float64(ms.CompletionTokens) / float64(total)
			n := int(frac * float64(barW))
			out += fmt.Sprintf("  %s %s %s\n", pad(ms.Model, 24), sChartTok.Render(strings.Repeat("█", n))+sRule.Render(strings.Repeat("░", barW-n)), sDim.Render(fmt.Sprintf("%.0f%%", frac*100)))
		}
	}
	return out
}

// ---------------------------------------------------------------- logs

func (m Model) logs() string {
	lines := m.snap.Logs
	if m.logFilter != "" {
		f := strings.ToLower(m.logFilter)
		var kept []string
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), f) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	h := m.bodyHeight()
	scroll := min(m.logScroll, max(0, len(lines)-h))
	end := len(lines) - scroll
	start := max(0, end-h)
	var b strings.Builder
	for _, l := range lines[start:end] {
		b.WriteString(styleLogLine(l))
		b.WriteByte('\n')
	}
	if len(lines) == 0 {
		b.WriteString(sDim.Render("  no log lines"))
	}
	return b.String()
}

func styleLogLine(l string) string {
	low := strings.ToLower(l)
	switch {
	case strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "-> 5"):
		return sErr.Render(l)
	case strings.Contains(low, "warning") || strings.Contains(low, "-> 4") || strings.Contains(low, "deprecated"):
		return sWarn.Render(l)
	}
	return l
}

// ---------------------------------------------------------------- helpers

func statusText(r stats.Request) string {
	switch {
	case r.Status != 0:
		return fmt.Sprint(r.Status)
	case r.Error != "":
		return "ERR"
	case !r.Done:
		return "…"
	}
	return "-"
}

func statusStyle(r stats.Request) lipgloss.Style {
	switch {
	case r.IsError():
		return sErr
	case !r.Done:
		return sWarn
	}
	return sOK
}

func pad(s string, w int) string {
	if lipgloss.Width(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func durOrDash(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return humanDur(d)
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func humanInt(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1e6)
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2fGB", float64(n)/(1<<30))
}

func maxInt(xs []int) int {
	m := 0
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

var sparkRunes = []rune(" ▁▂▃▄▅▆▇█")

func sparkline(xs []int) string {
	top := maxInt(xs)
	out := make([]rune, len(xs))
	for i, x := range xs {
		switch {
		case top == 0 || x == 0:
			out[i] = '·'
		default:
			// Any non-zero value gets at least the lowest bar.
			out[i] = sparkRunes[1+(x*(len(sparkRunes)-2))/top]
		}
	}
	return string(out)
}

// ---------------------------------------------------------------- styles

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5B3CC4", Dark: "#A78BFA"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B93A1"}
	cRule   = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#3F4652"}
	cOK     = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cErr    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cReq    = lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#38BDF8"}
	cSelBg  = lipgloss.AdaptiveColor{Light: "#E9E3FF", Dark: "#2E2650"}

	sBrand     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent)
	sDim       = lipgloss.NewStyle().Foreground(cDim)
	sRule      = lipgloss.NewStyle().Foreground(cRule)
	sLabel     = lipgloss.NewStyle().Foreground(cDim).Bold(true)
	sValue     = lipgloss.NewStyle().Bold(true)
	sErrValue  = lipgloss.NewStyle().Bold(true).Foreground(cErr)
	sOK        = lipgloss.NewStyle().Foreground(cOK)
	sWarn      = lipgloss.NewStyle().Foreground(cWarn)
	sErr       = lipgloss.NewStyle().Foreground(cErr)
	sSection   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sChartTok  = lipgloss.NewStyle().Foreground(cAccent)
	sChartReq  = lipgloss.NewStyle().Foreground(cReq)
	sTab       = lipgloss.NewStyle().Foreground(cDim)
	sTabActive = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent)
	sSelected  = lipgloss.NewStyle().Background(cSelBg).Bold(true)
	sTile      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cRule).Padding(0, 1).Width(14)
)
