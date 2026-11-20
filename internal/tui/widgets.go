package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

// ---------------------------------------------------------------- layout

// clip truncates s to width w.
func clip(s string, w int) string { return ansi.Truncate(s, max(0, w), "…") }

// pad left-aligns s in exactly w cells, truncating if needed.
func pad(s string, w int) string {
	if lipgloss.Width(s) > w {
		return clip(s, w)
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// padLeft right-aligns s in exactly w cells, truncating if needed.
func padLeft(s string, w int) string {
	if lipgloss.Width(s) > w {
		return clip(s, w)
	}
	return strings.Repeat(" ", w-lipgloss.Width(s)) + s
}

// fitHeight pads or cuts s to exactly h lines, each truncated to width w.
func fitHeight(s string, h, w int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = clip(l, w)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// spread places left and right at the two ends of a w-wide line, filling the
// gap with spaces in fill's style. right is dropped when there is no room.
func spread(left, right string, w int, fill lipgloss.Style) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		right = ""
		gap = w - lipgloss.Width(left)
	}
	if gap < 0 {
		return ansi.Truncate(left, max(0, w), "")
	}
	return left + fill.Render(strings.Repeat(" ", gap)) + right
}

// split divides w into n near-equal widths that sum to w.
func split(w, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = w / n
		if i < w%n {
			out[i]++
		}
	}
	return out
}

// joinH places equal-height blocks side by side.
func joinH(blocks ...string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

// joinV stacks blocks, skipping empty ones.
func joinV(blocks ...string) string {
	blocks = slices.DeleteFunc(blocks, func(s string) bool { return s == "" })
	return strings.Join(blocks, "\n")
}

// box draws content in a rounded panel exactly w×h cells, with title set into
// the top border and an optional dim meta label at the border's right end.
// Content is clipped and padded to the inner area.
func box(title, meta, content string, w, h int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	if w < 8 || h < 3 {
		return fitHeight(content, h, w)
	}
	cw := w - 4 // inner width, inside the border and one space of padding

	avail := w - 4 // top border minus corners and one rule each side
	if lipgloss.Width(title)+2 > avail {
		title = clip(title, avail-2)
	}
	titleSeg := " " + sTitle.Render(title) + " "
	metaSeg := ""
	if meta != "" && lipgloss.Width(titleSeg)+lipgloss.Width(meta)+3 <= avail {
		metaSeg = " " + sDim.Render(meta) + " "
	}
	fill := avail - lipgloss.Width(titleSeg) - lipgloss.Width(metaSeg)

	lines := make([]string, 0, h)
	lines = append(lines, sRule.Render("╭─")+titleSeg+sRule.Render(strings.Repeat("─", fill))+metaSeg+sRule.Render("─╮"))
	body := strings.Split(content, "\n")
	side := sRule.Render("│")
	for i := range h - 2 {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		lines = append(lines, side+" "+pad(l, cw)+" "+side)
	}
	lines = append(lines, sRule.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- tiles

// kpi is one headline figure.
type kpi struct {
	label, value, sub string
	style             lipgloss.Style
}

const tileHeight = 4

// tileRow renders as many of ts as fit in one row of equal-width tiles; ts is
// ordered most important first.
func tileRow(ts []kpi, w int) string {
	n := max(1, min(len(ts), w/16))
	ws := split(w, n)
	blocks := make([]string, n)
	for i := range n {
		blocks[i] = box(ts[i].label, "", ts[i].style.Bold(true).Render(ts[i].value)+"\n"+sDim.Render(ts[i].sub), ws[i], tileHeight)
	}
	return joinH(blocks...)
}

// ---------------------------------------------------------------- tables

// column describes one table column. A flex column absorbs spare width.
type column struct {
	title string
	w     int
	right bool
	flex  bool
}

type table struct {
	cols []column
	gap  int
}

// newTable fits cols into width w: the gap tightens first, then trailing
// columns are dropped, so order columns most important first.
func newTable(w int, cols ...column) table {
	cols = slices.Clone(cols)
	gap := 2
	for {
		total := gap * (len(cols) - 1)
		for _, c := range cols {
			total += c.w
		}
		if total <= w || len(cols) == 1 {
			for i := range cols {
				if cols[i].flex && total < w {
					cols[i].w += w - total
				}
			}
			return table{cols: cols, gap: gap}
		}
		if gap > 1 {
			gap = 1
			continue
		}
		cols = cols[:len(cols)-1]
	}
}

func (t table) row(cells ...string) string {
	parts := make([]string, len(t.cols))
	for i, c := range t.cols {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if c.right {
			parts[i] = padLeft(cell, c.w)
		} else {
			parts[i] = pad(cell, c.w)
		}
	}
	return strings.Join(parts, strings.Repeat(" ", t.gap))
}

func (t table) head() string {
	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = c.title
	}
	return sHead.Render(t.row(titles...))
}

// selectedRow renders a row as a full-width highlight bar.
func selectedRow(row string, w int) string {
	return sSelected.Render(pad(ansi.Strip(row), w))
}

// ---------------------------------------------------------------- charts

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

// paintChart colors one barChart row: bars in st, empty slots as a faint
// baseline.
func paintChart(row string, st lipgloss.Style) string {
	var b strings.Builder
	rs := []rune(row)
	for i := 0; i < len(rs); {
		j, zero := i, rs[i] == '·'
		for j < len(rs) && (rs[j] == '·') == zero {
			j++
		}
		if zero {
			b.WriteString(sRule.Render(strings.Repeat("▁", j-i)))
		} else {
			b.WriteString(st.Render(string(rs[i:j])))
		}
		i = j
	}
	return b.String()
}

// chartPanel is a panel holding a time-series bar chart of the newest vals
// that fit, with a peak/zero y-axis and a time span underneath. unit names one
// step of vals ("s", "m"). minSpan left-pads short series with empty slots so
// a young session does not render as a few giant bars.
func chartPanel(title, meta string, vals []int, unit string, minSpan int, st lipgloss.Style, w, h int) string {
	const gutter = 7
	plotW, ih := w-4-gutter, h-2
	if plotW < 4 || ih < 1 {
		return box(title, meta, "", w, h)
	}
	rows, axis := ih, false
	if ih >= 3 {
		rows, axis = ih-1, true
	}

	if want := min(minSpan, plotW); len(vals) < want {
		vals = append(make([]int, want-len(vals)), vals...)
	}
	n := min(len(vals), plotW)
	vals = vals[len(vals)-n:]
	// Stretch the n values across the full plot width.
	wide := make([]int, 0, plotW)
	for i, v := range vals {
		for range (i+1)*plotW/n - i*plotW/n {
			wide = append(wide, v)
		}
	}
	peak := maxInt(vals)

	lines := make([]string, 0, ih)
	for i, row := range barChart(wide, rows) {
		label := ""
		switch {
		case i == rows-1:
			label = "0"
		case i == 0 && peak > 0:
			label = humanInt(peak)
		}
		lines = append(lines, sDim.Render(padLeft(label, gutter-1))+" "+paintChart(row, st))
	}
	if axis && len(wide) >= 10 {
		span := fmt.Sprintf("-%d%s", n, unit)
		lines = append(lines, strings.Repeat(" ", gutter)+sDim.Render(pad(span, len(wide)-3)+"now"))
	}
	return box(title, meta, strings.Join(lines, "\n"), w, h)
}

// meter is a horizontal share bar w cells wide.
func meter(frac float64, w int) string {
	n := min(w, max(0, int(frac*float64(w)+0.5)))
	return sChartA.Render(strings.Repeat("━", n)) + sRule.Render(strings.Repeat("━", w-n))
}

// ---------------------------------------------------------------- formatting

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

func inCell(r stats.Request) string {
	if r.HasUsage {
		return humanInt(r.PromptTokens)
	}
	return "-"
}

func outCell(r stats.Request) string {
	switch {
	case r.HasUsage:
		return humanInt(r.CompletionTokens)
	case r.CompletionTokens > 0:
		return "~" + humanInt(r.CompletionTokens)
	}
	return "-"
}

func tpsCell(v float64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", v)
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

// ---------------------------------------------------------------- styles

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#2F5FD0", Dark: "#7AA2F7"}
	cSecond = lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#73DACA"}
	cText   = lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#C0CAF5"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#7A8499"}
	cRule   = lipgloss.AdaptiveColor{Light: "#CBD2DC", Dark: "#3B4261"}
	cOK     = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#9ECE6A"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#E0AF68"}
	cErr    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F7768E"}
	cBar    = lipgloss.AdaptiveColor{Light: "#E8EBF0", Dark: "#1F2335"}
	cSelBg  = lipgloss.AdaptiveColor{Light: "#DBE4FF", Dark: "#283457"}
	cOnAcc  = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1B26"}

	sText      = lipgloss.NewStyle().Foreground(cText)
	sDim       = lipgloss.NewStyle().Foreground(cDim)
	sRule      = lipgloss.NewStyle().Foreground(cRule)
	sTitle     = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sHead      = lipgloss.NewStyle().Foreground(cDim).Bold(true)
	sLabel     = lipgloss.NewStyle().Foreground(cDim)
	sValue     = lipgloss.NewStyle().Foreground(cText)
	sKey       = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sOK        = lipgloss.NewStyle().Foreground(cOK)
	sWarn      = lipgloss.NewStyle().Foreground(cWarn)
	sErr       = lipgloss.NewStyle().Foreground(cErr)
	sChartA    = lipgloss.NewStyle().Foreground(cAccent)
	sChartB    = lipgloss.NewStyle().Foreground(cSecond)
	sBrand     = lipgloss.NewStyle().Bold(true).Foreground(cOnAcc).Background(cAccent)
	sTab       = lipgloss.NewStyle().Foreground(cText)
	sTabActive = lipgloss.NewStyle().Bold(true).Foreground(cOnAcc).Background(cAccent)
	sSelected  = lipgloss.NewStyle().Foreground(cText).Background(cSelBg).Bold(true)
)
