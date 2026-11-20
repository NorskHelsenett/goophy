package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/NorskHelsenett/goophy/internal/stats"
)

// stackHeights splits the space under a tile row between a chart panel and a
// bottom area, giving up the bottom first and the chart second as room runs out.
func stackHeights(rem int) (chartH, bottomH int) {
	switch {
	case rem < 5:
		return 0, rem
	case rem < 8:
		return rem, 0
	}
	chartH = min(max(rem*45/100, 5), 12)
	if rem-chartH < 4 { // too short for a table: header plus one row
		return rem, 0
	}
	return chartH, rem - chartH
}

// empty is the placeholder line for a panel with nothing to show.
func empty(msg string) string { return sDim.Render(msg) }

// ---------------------------------------------------------------- overview

func (m Model) overview(w, h int) string {
	s := m.snap
	t := s.Totals

	est := ""
	if t.Estimated {
		est = "~"
	}
	errSub, errStyle := "none", sValue
	if t.Requests > 0 {
		errSub = fmt.Sprintf("%.1f%% rate", 100*float64(t.Errors)/float64(t.Requests))
	}
	if t.Errors > 0 {
		errStyle = sErr
	}
	tiles := []kpi{
		{"Requests", humanInt(t.Requests + len(s.Active)), fmt.Sprintf("%d in flight", len(s.Active)), sValue},
		{"Tokens out", est + humanInt(t.CompletionTokens), "completion", sValue},
		{"Throughput", fmt.Sprintf("%.1f", s.LiveTokensPerSecond(5)), "tok/s (5s)", sChartA},
		{"Errors", humanInt(t.Errors), errSub, errStyle},
		{"Tokens in", humanInt(t.PromptTokens), "prompt", sValue},
		{"Uptime", humanDur(s.Now.Sub(s.Started)), humanBytes(t.Bytes) + " sent", sValue},
	}

	chartH, bottomH := stackHeights(h - tileHeight)

	charts := ""
	if chartH > 0 {
		series := s.Series[:max(0, len(s.Series)-1)] // drop the still-filling second
		tok := make([]int, len(series))
		req := make([]int, len(series))
		errs := 0
		for i, bk := range series {
			tok[i], req[i] = bk.Chunks, bk.Requests
			errs += bk.Errors
		}
		reqMeta := "peak " + humanInt(maxInt(req)) + "/s"
		if errs > 0 {
			reqMeta = fmt.Sprintf("%d errors · %s", errs, reqMeta)
		}
		if w >= 90 {
			lw := w * 2 / 3
			charts = joinH(
				chartPanel("Throughput", "tok/s · peak "+humanInt(maxInt(tok)), tok, "s", 0, sChartA, lw, chartH),
				chartPanel("Requests", reqMeta, req, "s", 0, sChartB, w-lw, chartH),
			)
		} else {
			charts = chartPanel("Throughput", "tok/s · peak "+humanInt(maxInt(tok)), tok, "s", 0, sChartA, w, chartH)
		}
	}

	bottom := ""
	switch {
	case bottomH <= 0:
	case w >= 100:
		lw := w * 2 / 5
		bottom = joinH(m.inFlightPanel(lw, bottomH), m.recentPanel(w-lw, bottomH))
	case bottomH >= 8:
		ah := min(max(len(s.Active)+3, 4), bottomH/2)
		bottom = joinV(m.inFlightPanel(w, ah), m.recentPanel(w, bottomH-ah))
	case len(s.Active) > 0:
		bottom = m.inFlightPanel(w, bottomH)
	default:
		bottom = m.recentPanel(w, bottomH)
	}

	return joinV(tileRow(tiles, w), charts, bottom)
}

func (m Model) inFlightPanel(w, h int) string {
	active := m.snap.Active
	meta := ""
	if len(active) > 0 {
		meta = fmt.Sprint(len(active))
	}
	if len(active) == 0 {
		return box("In flight", meta, empty("Idle — waiting for requests"), w, h)
	}
	t := newTable(w-4,
		column{title: "", w: 1},
		column{title: "MODEL", w: 14, flex: true},
		column{title: "ELAPSED", w: 7, right: true},
		column{title: "TOKENS", w: 7, right: true},
		column{title: "TOK/S", w: 6, right: true},
	)
	lines := []string{t.head()}
	for i := len(active) - 1; i >= 0; i-- {
		r := active[i]
		dot, live := sWarn.Render("○"), 0.0
		if r.TTFB > 0 {
			dot = sOK.Render("●")
			if r.Duration > r.TTFB && r.Chunks > 0 {
				live = float64(r.Chunks) / (r.Duration - r.TTFB).Seconds()
			}
		}
		lines = append(lines, t.row(dot, orDash(r.Model), humanDur(r.Duration), humanInt(r.Chunks), tpsCell(live)))
	}
	return box("In flight", meta, strings.Join(lines, "\n"), w, h)
}

func (m Model) recentPanel(w, h int) string {
	recent := m.snap.Recent
	if len(recent) == 0 {
		return box("Recent requests", "", empty("Nothing yet — point a client at "+m.snap.Info.Listen), w, h)
	}
	t := newTable(w-4,
		column{title: "TIME", w: 8},
		column{title: "STATUS", w: 6},
		column{title: "MODEL", w: 14, flex: true},
		column{title: "DUR", w: 7, right: true},
		column{title: "OUT", w: 7, right: true},
		column{title: "TOK/S", w: 6, right: true},
		column{title: "IN", w: 7, right: true},
		column{title: "CLIENT", w: 14},
	)
	lines := []string{t.head()}
	for _, r := range recent[:min(len(recent), max(0, h-3))] {
		lines = append(lines, t.row(
			sDim.Render(r.Start.Format("15:04:05")), statusStyle(r).Render(statusText(r)), orDash(r.Model),
			humanDur(r.Duration), outCell(r), tpsCell(r.TokensPerSecond()), inCell(r), sDim.Render(orDash(r.UserAgent)),
		))
	}
	return box("Recent requests", "", strings.Join(lines, "\n"), w, h)
}

// ---------------------------------------------------------------- usage

func (m Model) usage(w, h int) string {
	s := m.snap
	t := s.Totals

	est := ""
	if t.Estimated {
		est = "~"
	}
	cachedSub, perPrompt, perCompletion := "not reported", "-", "-"
	if t.CachedTokens > 0 && t.PromptTokens > 0 {
		cachedSub = fmt.Sprintf("%.0f%% hit rate", 100*float64(t.CachedTokens)/float64(t.PromptTokens))
	}
	if t.Requests > 0 {
		perPrompt = humanInt(t.PromptTokens / t.Requests)
		perCompletion = est + humanInt(t.CompletionTokens/t.Requests)
	}
	tiles := []kpi{
		{"Total", est + humanInt(t.PromptTokens+t.CompletionTokens), humanInt(t.Requests) + " requests", sChartA},
		{"Prompt", humanInt(t.PromptTokens), "tokens in", sValue},
		{"Completion", est + humanInt(t.CompletionTokens), "tokens out", sValue},
		{"Cached", humanInt(t.CachedTokens), cachedSub, sValue},
		{"Avg in", perPrompt, "per request", sValue},
		{"Avg out", perCompletion, "per request", sValue},
	}

	chartH, bottomH := stackHeights(h - tileHeight)

	chart := ""
	if chartH > 0 {
		total := make([]int, len(s.Minutes))
		for i, mb := range s.Minutes {
			total[i] = mb.PromptTokens + mb.CompletionTokens
		}
		chart = chartPanel("Tokens per minute", "prompt + completion · peak "+humanInt(maxInt(total)),
			total, "m", 60, sChartA, w, chartH)
	}

	bottom := ""
	switch {
	case bottomH <= 0:
	case w >= 120:
		lw := w / 2
		bottom = joinH(m.clientsPanel(lw, bottomH), m.largestPanel(w-lw, bottomH))
	case bottomH >= 8:
		ch := bottomH / 2
		bottom = joinV(m.clientsPanel(w, ch), m.largestPanel(w, bottomH-ch))
	default:
		bottom = m.clientsPanel(w, bottomH)
	}

	return joinV(tileRow(tiles, w), chart, bottom)
}

func (m Model) clientsPanel(w, h int) string {
	s := m.snap
	if len(s.Clients) == 0 {
		return box("By client", "", empty("No finished requests yet"), w, h)
	}
	const barW = 12
	t := newTable(w-4,
		column{title: "CLIENT", w: 14, flex: true},
		column{title: "SHARE", w: barW + 5},
		column{title: "REQS", w: 6, right: true},
		column{title: "PROMPT", w: 8, right: true},
		column{title: "COMPL", w: 8, right: true},
		column{title: "CACHED", w: 8, right: true},
		column{title: "ERR", w: 5, right: true},
	)
	sum := s.Totals.PromptTokens + s.Totals.CompletionTokens
	lines := []string{t.head()}
	for _, c := range s.Clients {
		frac := 0.0
		if sum > 0 {
			frac = float64(c.PromptTokens+c.CompletionTokens) / float64(sum)
		}
		cached, errs := "-", humanInt(c.Errors)
		if c.CachedTokens > 0 {
			cached = humanInt(c.CachedTokens)
		}
		if c.Errors > 0 {
			errs = sErr.Render(errs)
		}
		lines = append(lines, t.row(
			c.Client, meter(frac, barW)+sDim.Render(fmt.Sprintf(" %3.0f%%", frac*100)),
			humanInt(c.Requests), humanInt(c.PromptTokens), humanInt(c.CompletionTokens), cached, errs,
		))
	}
	return box("By client", fmt.Sprint(len(s.Clients)), strings.Join(lines, "\n"), w, h)
}

// largestPanel lists the biggest prompts: where the context goes.
func (m Model) largestPanel(w, h int) string {
	big := slices.Clone(m.snap.Recent)
	big = slices.DeleteFunc(big, func(r stats.Request) bool { return !r.HasUsage })
	slices.SortFunc(big, func(a, b stats.Request) int { return b.PromptTokens - a.PromptTokens })
	if len(big) == 0 {
		return box("Largest prompts", "", empty("No reported usage yet"), w, h)
	}
	t := newTable(w-4,
		column{title: "TIME", w: 8},
		column{title: "MODEL", w: 14, flex: true},
		column{title: "IN", w: 8, right: true},
		column{title: "OUT", w: 8, right: true},
		column{title: "CLIENT", w: 14},
	)
	lines := []string{t.head()}
	for _, r := range big[:min(len(big), max(0, h-3))] {
		lines = append(lines, t.row(
			sDim.Render(r.Start.Format("15:04:05")), orDash(r.Model),
			sTitle.Render(humanInt(r.PromptTokens)), humanInt(r.CompletionTokens), sDim.Render(orDash(r.UserAgent)),
		))
	}
	return box("Largest prompts", "", strings.Join(lines, "\n"), w, h)
}

// ---------------------------------------------------------------- requests

const (
	detailSideMin = 160 // terminal width from which the inspector sits beside the list
	detailHeight  = 10  // inspector height when it sits under the list
)

func (m Model) requests(w, h int) string {
	list := m.requestList()
	if len(list) == 0 {
		return box("Requests", "", empty("No requests yet — point a client at "+m.snap.Info.Listen), w, h)
	}
	sel := max(0, indexOf(list, m.selID))

	lw, lh := w, h
	side := m.detail && w >= detailSideMin
	below := m.detail && !side && h >= detailHeight+6
	switch {
	case side:
		lw = w - min(60, w/3)
	case below:
		lh = h - detailHeight
	}

	t := newTable(lw-4,
		column{title: "TIME", w: 8},
		column{title: "STATUS", w: 6},
		column{title: "METHOD", w: 6},
		column{title: "PATH", w: 14, flex: true},
		column{title: "MODEL", w: 18},
		column{title: "DUR", w: 7, right: true},
		column{title: "OUT", w: 7, right: true},
		column{title: "TOK/S", w: 6, right: true},
		column{title: "TTFB", w: 7, right: true},
		column{title: "IN", w: 7, right: true},
	)
	rowsH := max(1, lh-3)
	start := 0 // scroll so the selection stays visible
	if sel >= rowsH {
		start = sel - rowsH + 1
	}
	lines := []string{t.head()}
	for i := start; i < min(len(list), start+rowsH); i++ {
		r := list[i]
		row := t.row(
			sDim.Render(r.Start.Format("15:04:05")), statusStyle(r).Render(statusText(r)), r.Method, r.Path, orDash(r.Model),
			humanDur(r.Duration), outCell(r), tpsCell(r.TokensPerSecond()), durOrDash(r.TTFB), inCell(r),
		)
		if i == sel {
			row = selectedRow(row, lw-4)
		}
		lines = append(lines, row)
	}

	meta := fmt.Sprintf("%d/%d", sel+1, len(list))
	if n := len(m.snap.Active); n > 0 {
		meta = fmt.Sprintf("%d active · %s", n, meta)
	}
	if m.selID == 0 {
		meta = "following newest · " + meta
	}
	listBox := box("Requests", meta, strings.Join(lines, "\n"), lw, lh)

	switch {
	case side:
		return joinH(listBox, m.requestDetail(list[sel], w-lw, h))
	case below:
		return joinV(listBox, m.requestDetail(list[sel], w, detailHeight))
	}
	return listBox
}

func (m Model) requestDetail(r stats.Request, w, h int) string {
	kv := func(k, v string) string { return sLabel.Render(pad(k, 9)) + v }

	route := r.Method + " " + r.Path
	if r.UpstreamPath != "" && r.UpstreamPath != r.Path {
		route += sDim.Render(" → ") + r.UpstreamPath
	}
	state := "done"
	if !r.Done {
		state = "in flight"
	}
	if r.Error != "" {
		state = sErr.Render(r.Error)
	}
	if r.Note != "" {
		state += sDim.Render(" · " + r.Note)
	}
	mode := "buffered"
	if r.Streaming {
		mode = "streamed"
	}
	tokens := sDim.Render("not reported by upstream")
	switch {
	case r.HasUsage:
		tokens = fmt.Sprintf("%s prompt + %s completion", humanInt(r.PromptTokens), humanInt(r.CompletionTokens))
		if r.CachedTokens > 0 {
			tokens += sDim.Render(fmt.Sprintf(" · %s cached", humanInt(r.CachedTokens)))
		}
	case r.Chunks > 0:
		tokens = fmt.Sprintf("~%s", humanInt(r.Chunks)) + sDim.Render(" estimated from chunks")
	}
	speed := "-"
	if v := r.TokensPerSecond(); v > 0 {
		speed = fmt.Sprintf("%.1f tok/s", v)
	}

	lines := []string{
		kv("route", route),
		kv("model", orDash(r.Model)),
		kv("client", orDash(r.UserAgent)),
		kv("status", statusStyle(r).Render(statusText(r))+"  "+state),
		kv("timing", fmt.Sprintf("first byte %s · total %s", durOrDash(r.TTFB), humanDur(r.Duration))),
		kv("speed", speed+sDim.Render(" · "+mode)),
		kv("tokens", tokens),
		kv("size", fmt.Sprintf("%s · %d chunks", humanBytes(r.Bytes), r.Chunks)),
	}
	return box(fmt.Sprintf("Request #%d", r.ID), r.Start.Format("15:04:05"), strings.Join(lines, "\n"), w, h)
}

// ---------------------------------------------------------------- models

func (m Model) models(w, h int) string {
	ms := m.snap.Models
	if len(ms) == 0 {
		return box("Models", "", empty("No model usage yet this session"), w, h)
	}

	busy := map[string]int{}
	for _, r := range m.snap.Active {
		busy[r.Model]++
	}
	total := 0
	for _, s := range ms {
		total += s.CompletionTokens
	}

	const barW = 12
	t := newTable(w-4,
		column{title: "", w: 1},
		column{title: "MODEL", w: 18, flex: true},
		column{title: "OUTPUT SHARE", w: barW + 5},
		column{title: "REQS", w: 6, right: true},
		column{title: "OUT", w: 8, right: true},
		column{title: "TOK/S", w: 7, right: true},
		column{title: "TTFB", w: 8, right: true},
		column{title: "IN", w: 8, right: true},
		column{title: "ERR", w: 5, right: true},
		column{title: "LAST USED", w: 10, right: true},
	)
	lines := []string{t.head()}
	for _, s := range ms {
		dot := " "
		if busy[s.Model] > 0 {
			dot = sOK.Render("●")
		}
		frac := 0.0
		if total > 0 {
			frac = float64(s.CompletionTokens) / float64(total)
		}
		out, errs := humanInt(s.CompletionTokens), humanInt(s.Errors)
		if s.Estimated {
			out = "~" + out
		}
		if s.Errors > 0 {
			errs = sErr.Render(errs)
		}
		lines = append(lines, t.row(
			dot, s.Model, meter(frac, barW)+sDim.Render(fmt.Sprintf(" %3.0f%%", frac*100)),
			humanInt(s.Requests), out, tpsCell(s.AvgTokensPerSecond()), durOrDash(s.AvgTTFB()),
			humanInt(s.PromptTokens), errs, sDim.Render(humanDur(m.snap.Now.Sub(s.LastUsed))+" ago"),
		))
	}
	meta := fmt.Sprintf("%d used this session", len(ms))
	if n := len(m.snap.Active); n > 0 {
		meta += fmt.Sprintf(" · %d active", n)
	}
	return box("Models", meta, strings.Join(lines, "\n"), w, h)
}

// ---------------------------------------------------------------- logs

func (m Model) logs(w, h int) string {
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

	ih := max(1, h-2)
	scroll := min(m.logScroll, max(0, len(lines)-ih))
	end := len(lines) - scroll
	start := max(0, end-ih)

	meta := fmt.Sprintf("%d lines", len(lines))
	if m.logFilter != "" {
		meta = fmt.Sprintf("filter %q · %s", m.logFilter, meta)
	}
	if scroll > 0 {
		meta += fmt.Sprintf(" · %d below", scroll)
	} else {
		meta += " · following"
	}

	if len(lines) == 0 {
		return box("Logs", meta, empty("No log lines"), w, h)
	}
	out := make([]string, 0, ih)
	for _, l := range lines[start:end] {
		out = append(out, styleLogLine(l))
	}
	return box("Logs", meta, strings.Join(out, "\n"), w, h)
}

func styleLogLine(l string) string {
	l = strings.ReplaceAll(l, "\t", "    ")
	low := strings.ToLower(l)
	switch {
	case strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "-> 5"):
		return sErr.Render(l)
	case strings.Contains(low, "warning") || strings.Contains(low, "-> 4") || strings.Contains(low, "deprecated"):
		return sWarn.Render(l)
	}
	return sText.Render(l)
}
