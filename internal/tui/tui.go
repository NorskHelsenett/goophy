// Package tui renders goophy's interactive terminal dashboard: live session
// statistics, a request inspector, per-model usage and the log stream, driven
// by a stats.Collector.
//
// The screen is a fixed frame — status bar, view tabs, body, key hints — and
// every view fills the body with bordered panels sized to the terminal, so the
// layout never reflows or scrolls as data arrives.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

// refreshInterval is how often the dashboard re-reads the collector.
const refreshInterval = 250 * time.Millisecond

// Below this size the panels cannot be laid out; a hint is shown instead.
const (
	minWidth  = 50
	minHeight = 10
)

type tab int

const (
	tabOverview tab = iota
	tabUsage
	tabRequests
	tabModels
	tabLogs
)

var tabNames = []string{"Overview", "Usage", "Requests", "Models", "Logs"}

// StoppedMsg tells the dashboard the server has stopped; it quits on receipt.
type StoppedMsg struct{ Err error }

type tickMsg time.Time

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	c    *stats.Collector
	snap stats.Snapshot
	tab  tab
	w, h int

	// Requests view: selection is by request ID so it stays put as new
	// requests arrive. selID 0 follows the newest request.
	selID  uint64
	detail bool

	// Logs view: lines scrolled up from the bottom; 0 follows the tail.
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

	page := max(1, m.bodyHeight()-4)
	switch m.tab {
	case tabRequests:
		list := m.requestList()
		switch k.String() {
		case "up", "k":
			m.selID = moveSel(list, m.selID, -1)
		case "down", "j":
			m.selID = moveSel(list, m.selID, 1)
		case "pgup", "ctrl+u":
			m.selID = moveSel(list, m.selID, -page)
		case "pgdown", "ctrl+d":
			m.selID = moveSel(list, m.selID, page)
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

// bodyHeight is the space left for the active view under the status bar and
// tabs and above the key hints.
func (m Model) bodyHeight() int { return max(1, m.h-3) }

// View implements tea.Model.
func (m Model) View() string {
	w, h := m.w, m.bodyHeight()

	var body string
	switch {
	case m.w < minWidth || m.h < minHeight:
		body = sDim.Render(fmt.Sprintf(" terminal too small — needs %d×%d", minWidth, minHeight))
	case m.tab == tabOverview:
		body = m.overview(w, h)
	case m.tab == tabUsage:
		body = m.usage(w, h)
	case m.tab == tabRequests:
		body = m.requests(w, h)
	case m.tab == tabModels:
		body = m.models(w, h)
	case m.tab == tabLogs:
		body = m.logs(w, h)
	}

	return strings.Join([]string{
		clip(m.statusBar(), w),
		clip(m.tabBar(), w),
		fitHeight(body, h, w),
		clip(m.footer(), w),
	}, "\n")
}

// statusBar is the top line: identity and connection on the left, session
// state on the right, on a full-width tinted bar.
func (m Model) statusBar() string {
	s := m.snap
	on := func(st lipgloss.Style) lipgloss.Style { return st.Background(cBar) }
	sep := on(sRule).Render(" │ ")

	upStyle := sOK
	switch {
	case strings.HasPrefix(s.Upstream, "unreachable"):
		upStyle = sErr
	case s.Upstream == "unknown" || strings.HasPrefix(s.Upstream, "detecting"):
		upStyle = sWarn
	}

	left := sBrand.Render(" goophy ") +
		on(sDim).Render(" "+s.Info.Version+" ") + on(sWarn).Render("preview") + sep +
		on(upStyle).Render("● ") + on(sText).Render(s.Upstream) + sep +
		on(sText).Render(s.Info.Listen) + on(sDim).Render(" → ") + on(sText).Render(s.Info.Target)
	if !s.Info.APIKeySet {
		left += on(sDim).Render("  no API key")
	}

	right := on(sDim).Render("up ") + on(sText).Render(humanDur(s.Now.Sub(s.Started))) + on(sText).Render(" ")
	if s.UpdateNotice != "" {
		right = on(sWarn).Render("▲ update "+s.UpdateNotice+" · goophy update") + sep + right
	}
	return spread(left, right, m.w, on(sText))
}

// tabBar is the view switcher, with live activity on the right.
func (m Model) tabBar() string {
	parts := make([]string, len(tabNames))
	for i, n := range tabNames {
		if tab(i) == m.tab {
			parts[i] = sTabActive.Render(fmt.Sprintf(" %d %s ", i+1, n))
		} else {
			parts[i] = " " + sDim.Render(fmt.Sprint(i+1)) + " " + sTab.Render(n) + " "
		}
	}

	right := sDim.Render("idle ")
	if n := len(m.snap.Active); n > 0 {
		right = sOK.Render("● ") + sText.Render(fmt.Sprintf("%d in flight", n)) +
			sDim.Render(fmt.Sprintf(" · %.1f tok/s ", m.snap.LiveTokensPerSecond(5)))
	}
	return spread(strings.Join(parts, ""), right, m.w, lipgloss.NewStyle())
}

// footer is the key-hint line for the active view.
func (m Model) footer() string {
	if m.filtering {
		return sTabActive.Render(" filter ") + " " + m.logFilter + "█  " + hints("enter", "apply", "esc", "clear")
	}
	var keys []string
	switch m.tab {
	case tabRequests:
		keys = []string{"↑↓", "select", "enter", "details", "g", "newest"}
	case tabLogs:
		keys = []string{"↑↓ pgup pgdn", "scroll", "G", "follow", "/", "filter"}
		if m.logFilter != "" {
			keys = append(keys, "esc", "clear filter")
		}
	}
	keys = append(keys, "1-5", "view", "tab", "next", "q", "quit")
	return spread(" "+hints(keys...), sDim.Render(m.snap.Now.Format("15:04:05")+" "), m.w, lipgloss.NewStyle())
}

// hints renders alternating key/description pairs.
func hints(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, sKey.Render(kv[i])+" "+sDim.Render(kv[i+1]))
	}
	return strings.Join(parts, sRule.Render("  ·  "))
}
