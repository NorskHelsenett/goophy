package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

func populated() *stats.Collector {
	c := stats.New(stats.Info{Version: "test", Listen: "127.0.0.1:22434", Target: "http://upstream"})
	for i := range 30 {
		tr := c.Begin("POST", "/v1/chat/completions", "pi/1.5.0")
		tr.SetModel(fmt.Sprintf("model-%d", i%3))
		tr.SetStatus(200+i%2*300, true)
		tr.Wrote(10)
		tr.AddChunks(5)
		if i%4 == 0 {
			tr.SetUsage(100, 20, 50)
		}
		tr.Finish("")
	}
	active := c.Begin("POST", "/api/chat", "curl")
	active.SetModel("live")
	fmt.Fprintln(c, "ERROR RESPONSE (500): boom")
	fmt.Fprintln(c, "plain line")
	return c
}

// TestViewFitsTerminal renders every tab at several sizes: nothing may panic,
// and the output must never exceed the terminal's width or height.
func TestViewFitsTerminal(t *testing.T) {
	sizes := [][2]int{{20, 5}, {60, 15}, {130, 40}, {250, 70}}
	for _, c := range []*stats.Collector{stats.New(stats.Info{}), populated()} {
		for _, sz := range sizes {
			var m tea.Model = New(c)
			m, _ = m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m, _ = m.Update(tickMsg{})
			for tabN := range tabNames {
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune('1' + tabN)}})
				// Exercise navigation keys on each tab.
				for _, k := range []string{"j", "j", "k", "G", "g"} {
					m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
				}
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) > sz[1] {
					t.Errorf("%dx%d tab %d: %d lines", sz[0], sz[1], tabN, len(lines))
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz[0] {
						t.Errorf("%dx%d tab %d line %d: width %d: %q", sz[0], sz[1], tabN, i, w, l)
						break
					}
				}
			}
		}
	}
}

func TestLogFilter(t *testing.T) {
	var m tea.Model = New(populated())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m, _ = m.Update(tickMsg{})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "boom" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v := m.View()
	if !strings.Contains(v, "boom") || strings.Contains(v, "plain line") {
		t.Errorf("filter not applied:\n%s", v)
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]int{0, 1, 8}); got != "·▁█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline([]int{0, 0}); got != "··" {
		t.Errorf("sparkline = %q", got)
	}
}

func TestBarChart(t *testing.T) {
	got := barChart([]int{0, 4, 8}, 2)
	want := []string{"  █", "·██"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("barChart = %q, want %q", got, want)
	}
}
