package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const minW, minH = 80, 24

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "nltop"
	return v
}

// render lays the visible panels out the way btop does: the server box across
// the top, then two rows of two boxes. A hidden panel gives its room to its
// neighbour, a hidden row to the other row.
func (m *model) render() string {
	w, h := m.w, m.h
	if w == 0 || h == 0 {
		return ""
	}
	notice := func(s string) string {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, stLabel.Render(s))
	}
	if w < minW || h < minH {
		return notice(fmt.Sprintf("nltop needs %d×%d, this terminal is %d×%d", minW, minH, w, h))
	}

	hA := 0
	if m.show[pServer] {
		hA = 4
	}
	rowB := m.show[pSlots] || m.show[pTokens]
	rowC := m.show[pRequests] || m.show[pGPU]
	if hA == 0 && !rowB && !rowC {
		return notice("all panels hidden · press 1-5 to show one · q to quit")
	}
	avail := h - hA
	hB, hC := 0, 0
	switch {
	case rowB && rowC:
		hB = avail * 11 / 20
		if m.show[pSlots] {
			hB = max(hB, m.slotsWant()+2)
		}
		hB = min(max(hB, 8), avail-7)
		hC = avail - hB
	case rowB:
		hB = avail
	case rowC:
		hC = avail
	}

	var rows []string
	if hA > 0 {
		rows = append(rows, m.serverBox(w, hA))
	}
	if rowB {
		switch {
		case m.show[pSlots] && m.show[pTokens]:
			wS := min(max(w*2/5, 42), 62)
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, m.slotsBox(wS, hB), m.tokensBox(w-wS, hB)))
		case m.show[pSlots]:
			rows = append(rows, m.slotsBox(w, hB))
		default:
			rows = append(rows, m.tokensBox(w, hB))
		}
	}
	if rowC {
		switch {
		case m.show[pRequests] && m.show[pGPU]:
			wG := min(max(w*3/10, 34), 46)
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, m.requestsBox(w-wG, hC), m.gpuBox(wG, hC)))
		case m.show[pRequests]:
			rows = append(rows, m.requestsBox(w, hC))
		default:
			rows = append(rows, m.gpuBox(w, hC))
		}
	}
	out := strings.Join(rows, "\n")
	if !rowB && !rowC { // only the server box: the rest of the screen stays empty
		out += strings.Repeat("\n"+strings.Repeat(" ", w), h-hA)
	}
	return out
}

func panelTitle(num, name string) string {
	return stHi.Render(num) + stTitle.Render(name)
}

func kv(label, value string) string {
	return stLabel.Render(label+" ") + stText.Render(value)
}

func sep() string { return stDim.Render(" · ") }

// ───────────────────────────── ¹server ─────────────────────────────

// item is one piece of a server line. When the line is too long, items are
// left out whole, the least important (highest rank) first.
type item struct {
	rank int
	text string
}

func fitItems(items []item, w int) string {
	for {
		parts := make([]string, len(items))
		worst := 0
		for i, it := range items {
			parts[i] = it.text
			if it.rank > items[worst].rank {
				worst = i
			}
		}
		line := " " + strings.Join(parts, "  ")
		if width(line) <= w || len(items) <= 1 {
			return line
		}
		items = append(items[:worst:worst], items[worst+1:]...)
	}
}

func (m *model) serverBox(w, h int) string {
	var status string
	switch m.health {
	case healthOK:
		status = stOK.Render("● ok")
		if m.props != nil && m.props.Sleeping {
			status = stLabel.Render("○ sleeping")
		}
	case healthLoading:
		status = stWarn.Render("◌ loading model")
	case healthDown:
		status = stErr.Render("● unreachable") + " " + stLabel.Render(m.healthMsg)
	default:
		status = stDim.Render("○ connecting")
	}

	l1 := []item{{0, status}}
	if p := m.props; p != nil {
		name := p.ModelAlias
		if name == "" {
			name = filepath.Base(p.ModelPath)
		}
		l1 = append(l1, item{1, stTitle.Render(name)}, item{4, stText.Render(p.ModelFtype)})
		if mm := m.meta; mm != nil {
			l1 = append(l1, item{5, kv("params", paramCount(mm.NParams))}, item{6, kv("size", gib(float64(mm.Size)))},
				item{2, kv("ctx", commas(mm.NCtx))})
			if mm.NCtxTrain > 0 && mm.NCtxTrain != mm.NCtx {
				l1 = append(l1, item{8, kv("trained", commas(mm.NCtxTrain))})
			}
		} else {
			l1 = append(l1, item{2, kv("ctx", commas(p.Defaults.NCtx))})
		}
		l1 = append(l1, item{3, kv("slots", strconv.Itoa(p.TotalSlots))})
		var mod []string
		for _, x := range []struct {
			on   bool
			name string
		}{{p.Modalities.Vision, "vision"}, {p.Modalities.Video, "video"}, {p.Modalities.Audio, "audio"}} {
			if x.on {
				mod = append(mod, x.name)
			}
		}
		if len(mod) > 0 {
			l1 = append(l1, item{7, stLabel.Render(strings.Join(mod, " "))})
		}
	}

	var l2 []item
	if p := m.props; p != nil {
		l2 = append(l2, item{3, kv("build", p.BuildInfo)})
	}
	if m.health == healthOK && !m.procStart.IsZero() {
		l2 = append(l2, item{1, kv("up", dur(m.now.Sub(m.procStart)))})
	}
	l2 = append(l2, item{0, kv("at", m.cfg.url)}, item{5, kv("poll", m.cfg.interval.String())})
	if m.health == healthOK {
		l2 = append(l2, item{6, kv("answer", fmt.Sprintf("%.1f ms", float64(m.latency.Microseconds())/1000))})
	}
	if p := m.props; p != nil {
		if mt := m.metrics; mt != nil {
			l2 = append(l2,
				item{2, kv("queued", fmt.Sprintf("%.0f", mt["requests_deferred"]))},
				item{4, kv("since start", tokens(int(mt["prompt_tokens_total"]))+" prompt"+
					" · "+tokens(int(mt["tokens_predicted_total"]))+" gen")})
		} else if !p.EndpointMetrics {
			l2 = append(l2, item{7, stDim.Render("/metrics off")})
		}
	}

	top := edge{
		left: []string{panelTitle("¹", "server")},
		mid:  stTitle.Render(m.now.Format("15:04:05")),
		right: []string{
			stHi.Render("1-5") + stLabel.Render(" panels"),
			stHi.Render("q") + stLabel.Render(" quit"),
			stLabel.Render("nltop ") + stText.Render(m.cfg.version),
		},
	}
	return box(w, h, stBoxServer, top, edge{}, []string{fitItems(l1, w-2), fitItems(l2, w-2)})
}

// ───────────────────────────── ²slots ─────────────────────────────

// slotsWant is the number of lines the slots panel would like to have.
func (m *model) slotsWant() int {
	n := 0
	for _, s := range m.slots {
		if s.Processing {
			n += 5
		} else {
			n += 2
		}
	}
	if m.busy() {
		return n + 4 // blank, today
	}
	return n + 8 // blank, last, blank, today
}

func (m *model) slotsBox(w, h int) string {
	iw, ih := w-2, h-2
	nBusy := 0
	for _, s := range m.slots {
		if s.Processing {
			nBusy++
		}
	}
	top := edge{left: []string{panelTitle("²", "slots")}}
	if len(m.slots) > 0 {
		top.right = []string{stText.Render(fmt.Sprintf("%d of %d", nBusy, len(m.slots))) + stLabel.Render(" busy")}
	}

	var lines []string
	switch {
	case m.slotsErr != "":
		lines = append(lines, " "+stErr.Render("no slot data: ")+stLabel.Render(m.slotsErr))
		if strings.Contains(m.slotsErr, "501") {
			lines = append(lines, " "+stDim.Render("the server was started with --no-slots"))
		}
	case len(m.slots) == 0:
		lines = append(lines, " "+stDim.Render("waiting for the server"))
	default:
		// Full blocks when they fit, one line per slot when they do not.
		var full, compact []string
		for _, s := range m.slots {
			full = append(full, m.slotBlock(s, iw)...)
			compact = append(compact, m.slotLine(s, iw))
		}
		summary := m.summaryLines(nBusy == 0, true)
		tight := m.summaryLines(nBusy == 0, false)
		switch {
		case len(full)+len(summary) <= ih:
			lines = append(full, summary...)
		case len(full)+len(tight) <= ih:
			lines = append(full, tight...)
		case len(compact)+len(tight) <= ih:
			lines = append(compact, tight...)
		case len(compact) <= ih:
			lines = compact
		default:
			lines = append(compact[:ih-1], " "+stDim.Render(fmt.Sprintf("+%d more slots", len(compact)-ih+1)))
		}
	}
	return box(w, h, stBoxSlots, top, edge{}, lines)
}

// slotState is the coloured state of a slot: "● generating", "○ idle 7h 02m".
func (m *model) slotState(s Slot) string {
	ph := slotPhase(s)
	if ph != phIdle {
		st := phaseStyle(ph).Render("● " + phaseName(ph))
		if ph == phGen && strings.HasSuffix(strings.TrimSpace(s.Params.GenerationPrompt), "<think>") {
			// Thinking is on for this request; /slots cannot say which part is being written.
			st += stDim.Render(" thinking on")
		}
		return st
	}
	st := "○ idle"
	if t := m.slotIdleSince(s.ID); !t.IsZero() {
		st += " " + dur(m.now.Sub(t))
	}
	return stDim.Render(st)
}

// slotIdleSince is the end of the slot's newest request, as far as known.
func (m *model) slotIdleSince(slot int) time.Time {
	for i := len(m.reqs) - 1; i >= 0 && i >= len(m.reqs)-200; i-- {
		if m.reqs[i].Slot == slot {
			return m.reqs[i].End
		}
	}
	if len(m.slots) == 1 {
		return m.trk.lastActive
	}
	return time.Time{}
}

func ctxFrac(s Slot) float64 {
	if s.NCtx <= 0 {
		return 0
	}
	return float64(s.CtxTokens) / float64(s.NCtx)
}

// ctxGauge is the context meter with its numbers: the meter runs from teal to
// amber, so a context that fills up shades towards amber.
func ctxGauge(s Slot, w int) string {
	f := ctxFrac(s)
	nums := stText.Render(fitRight(tokens(s.CtxTokens)+"/"+tokens(s.NCtx), 11)) + " " +
		gradCtx.at(f).Render(fitRight(fmt.Sprintf("%.0f%%", f*100), 4))
	return meter(w-17, f, gradCtx) + " " + nums
}

// slotBlock describes one slot in two lines when idle and five when busy.
func (m *model) slotBlock(s Slot, iw int) []string {
	ph := slotPhase(s)
	tr := m.trk.slots[s.ID]
	lv := m.live[s.ID]
	if lv != nil && lv.Task != s.Task {
		lv = nil
	}

	// On the right: who is calling, or the task number when nobody is named,
	// and how long the request has been running. In a narrow panel the state
	// on the left wins and the running time goes first.
	left := " " + stLabel.Render("slot ") + stText.Render(strconv.Itoa(s.ID)) + " " + m.slotState(s)
	right := stDim.Render("task ") + stLabel.Render(strconv.Itoa(s.Task))
	if s.Processing {
		if name := m.liveCallers()[s.ID]; name != "" {
			right = stTitle.Render(name)
		}
		if start := m.slotStart(s); !start.IsZero() {
			if long := right + sep() + stText.Render(dur(m.now.Sub(start))); width(left)+width(long)+2 <= iw {
				right = long
			}
		}
	}
	if width(left)+width(right)+2 > iw {
		right = ""
	}
	out := []string{
		spread(left, right+" ", iw),
		" " + stLabel.Render("ctx ") + ctxGauge(s, iw-6),
	}
	if !s.Processing {
		return out
	}

	prompt := commas(s.PromptProcessed)
	if lv != nil && ph == phPrompt && lv.PromptTotal(s.PromptCached) > s.PromptProcessed {
		prompt += " of ~" + tokens(lv.PromptTotal(s.PromptCached))
	} else {
		prompt += " new"
	}
	prompt += " · " + commas(s.PromptCached) + " cached"
	pRate := ""
	if tr != nil && ph == phPrompt && tr.promptRate > 0 {
		pRate = phaseStyle(phPrompt).Render(rate(tr.promptRate) + " t/s")
	} else if tr != nil && tr.promptAvg > 0 {
		pRate = stDim.Render(rate(tr.promptAvg) + " t/s")
	}
	gen := commas(s.Decoded())
	if s.Params.MaxTokens > 0 {
		gen += " of " + commas(s.Params.MaxTokens)
	}
	gRate := ""
	if tr != nil && tr.genRate > 0 {
		gRate = phaseStyle(ph).Render(rate(tr.genRate) + " t/s")
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', 3, 64) }
	sampler := []string{"temp " + f(s.Params.Temperature), "top_k " + strconv.Itoa(s.Params.TopK),
		"top_p " + f(s.Params.TopP), "min_p " + f(s.Params.MinP)}
	samplers := strings.Join(sampler, " · ")
	if len(samplers)+1 > iw {
		samplers = strings.Join(sampler, " ")
	}
	return append(out,
		spread(" "+stLabel.Render("prompt ")+stText.Render(prompt), pRate+" ", iw),
		spread(" "+stLabel.Render("gen    ")+stText.Render(gen), gRate+" ", iw),
		" "+stDim.Render(samplers),
	)
}

// slotLine is the one-line form of a slot, for servers with many slots.
func (m *model) slotLine(s Slot, iw int) string {
	f := ctxFrac(s)
	right := gradCtx.at(f).Render(fitRight(fmt.Sprintf("%.0f%%", f*100), 4))
	if tr := m.trk.slots[s.ID]; tr != nil && tr.genRate > 0 {
		right += " " + phaseStyle(slotPhase(s)).Render(fitRight(rate(tr.genRate)+" t/s", 9))
	} else {
		right += strings.Repeat(" ", 10)
	}
	state := m.slotState(s)
	if name := m.liveCallers()[s.ID]; name != "" {
		state = phaseStyle(slotPhase(s)).Render("● ") + stTitle.Render(name) // the colour still tells the phase
	}
	left := " " + stText.Render(fitRight(strconv.Itoa(s.ID), 2)) + " " + fit(state, 16) + " "
	mw := max(4, iw-width(left)-width(right)-2)
	return left + meter(mw, f, gradCtx) + " " + right + " "
}

// noRequests is what the screen says when the journal has nothing to show.
func (m *model) noRequests() string {
	return "no request in the journal of " + m.cfg.unit + " since yesterday"
}

// summaryLines is what the slots panel shows below the slots: the last
// request while the server is idle, and today's totals. airy puts a blank line
// between the two.
func (m *model) summaryLines(withLast, airy bool) []string {
	const pad = "        "
	speed := func(p phase, tps float64) string {
		return stLabel.Render(" @ ") + phaseStyle(p).Render(rate(tps)) + stLabel.Render(" t/s")
	}
	out := []string{""}
	if withLast {
		if r, ok := m.lastRequest(); ok {
			when := r.Start.Format("15:04:05")
			if dayOf(r.Start) != dayOf(m.now) {
				when = r.Start.Format("Mon 15:04:05")
			}
			l := " " + stLabel.Render("last   ") + stText.Render(when) + sep() + stLabel.Render("took ") + stText.Render(dur(r.End.Sub(r.Start)))
			if r.Cancelled {
				l += sep() + stErr.Render("cancelled")
			}
			out = append(out, l,
				pad+stLabel.Render("prompt ")+stText.Render(commas(r.PromptTokens))+speed(phPrompt, r.PromptTPS),
				pad+stLabel.Render("gen    ")+stText.Render(commas(r.GenTokens))+speed(phGen, r.GenTPS))
		} else if m.journalNote != "" {
			// The first sentence says what is wrong; the requests panel has the rest.
			short, _, _ := strings.Cut(m.journalNote, ": ")
			out = append(out, " "+stLabel.Render("last   ")+stWarn.Render(short))
		} else {
			out = append(out, " "+stLabel.Render("last   ")+stDim.Render(m.noRequests()))
		}
		if airy {
			out = append(out, "")
		}
	}
	t := m.todayTotals()
	if t.n == 0 {
		return append(out, " "+stLabel.Render("today  ")+stDim.Render("no requests yet"))
	}
	avg := 0.0
	if t.genMs > 0 {
		avg = float64(t.gen) / t.genMs * 1000
	}
	return append(out,
		" "+stLabel.Render("today  ")+stText.Render(commas(t.n))+stLabel.Render(" requests")+sep()+
			stLabel.Render("busy ")+stText.Render(dur(time.Duration(t.busyMs)*time.Millisecond)),
		pad+stLabel.Render("prompt ")+stText.Render(tokens(t.prompt)),
		pad+stLabel.Render("gen    ")+stText.Render(tokens(t.gen))+speed(phGen, avg),
	)
}

// ───────────────────────────── ³tokens ─────────────────────────────

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// overlay writes text over line, starting at cell x.
func overlay(line, text string, x int) string {
	lw, tw := width(line), width(text)
	if x < 0 || x+tw > lw {
		return line
	}
	return ansi.Cut(line, 0, x) + text + ansi.Cut(line, x+tw, lw)
}

func (m *model) tokensBox(w, h int) string {
	iw, ih := w-2, h-2
	statsW := 0
	if iw >= 60 {
		statsW = 22
	}
	gw, gh := iw-statsW, ih
	if statsW == 0 {
		gh-- // the numbers go on a line above the graph
	}
	hTop := (gh + 1) / 2 // generation grows up from the middle
	hBot := gh - hTop    // prompt evaluation hangs down from it

	cols := 2 * gw
	span, skip, held, idleCols := m.trk.window(cols)
	gen, prompt, ph := m.trk.ser.columns(cols, span, skip)
	peakG, peakP := maxOf(gen), maxOf(prompt)
	scaleG, scaleP := niceCeil(peakG, 10), niceCeil(peakP, 100)
	seen := map[phase]bool{}
	for i := range gen {
		gen[i] /= scaleG
		prompt[i] /= scaleP
		if gen[i] > 0 {
			seen[ph[i]] = true
		}
	}
	graph := brailleGraph(gen, gw, hTop, false, func(c int) (gradient, int) { return phaseGradient(ph[c]), int(ph[c]) })
	graph = append(graph, brailleGraph(prompt, gw, hBot, true, func(int) (gradient, int) { return gradPrompt, 0 })...)
	// The scale of each half, at its far edge.
	graph[0] = overlay(graph[0], stDim.Render(" "+commas(int(scaleG))+" t/s "), 0)
	if hBot > 0 {
		graph[gh-1] = overlay(graph[gh-1], stDim.Render(" "+commas(int(scaleP))+" t/s "), 0)
	}

	idle := ""
	if !m.busy() && m.trk.idleTicks > 4 {
		idle = "idle"
		if t := m.idleSince(); !t.IsZero() {
			idle += " " + dur(m.now.Sub(t))
		}
		// Written into the idle stretch at the right, when it is wide enough.
		if n := idleCols / 2; hTop >= 3 && n >= len(idle)+6 {
			graph[hTop/2] = overlay(graph[hTop/2], stLabel.Render(" "+idle+" "), gw-n+(n-len(idle)-2)/2)
		}
	}

	// Current rates, summed over the slots.
	var curG, curP float64
	curPh := phIdle
	for _, s := range m.slots {
		tr := m.trk.slots[s.ID]
		if tr == nil || !s.Processing {
			continue
		}
		switch p := slotPhase(s); p {
		case phPrompt:
			curP += tr.promptRate
		default:
			curG += tr.genRate
			curPh = p
		}
	}
	t := m.todayTotals()

	var lines []string
	if statsW == 0 {
		lines = append(lines, " "+stLabel.Render("▲ gen ")+phaseStyle(curPh).Render(rate(curG))+stLabel.Render(" t/s")+
			sep()+stLabel.Render("▼ prompt ")+phaseStyle(phPrompt).Render(rate(curP))+stLabel.Render(" t/s"))
		lines = append(lines, graph...)
	} else {
		stats := make([]string, gh)
		block := func(at, limit int, arrow, name string, st lipgloss.Style, cur, peak float64, total int) {
			rows := []string{
				" " + st.Render(arrow+" "+name),
				" " + st.Bold(true).Render(fitRight(rate(cur), 9)) + stLabel.Render(" t/s"),
				" " + stLabel.Render("peak  ") + stText.Render(fitRight(rate(peak), 9)),
				" " + stLabel.Render("today ") + stText.Render(fitRight(tokens(total), 9)),
			}
			for i, r := range rows {
				if at+i < limit {
					stats[at+i] = r
				}
			}
		}
		gName := "gen"
		if curPh != phIdle {
			gName = phaseName(curPh)
		}
		block(0, hTop, "▲", gName, phaseStyle(curPh), curG, peakG, t.gen)
		pSt := stDim
		if curP > 0 {
			pSt = phaseStyle(phPrompt)
		}
		block(hTop, gh, "▼", "prompt", pSt, curP, peakP, t.prompt)
		for i := range graph {
			lines = append(lines, graph[i]+fit(stats[i], statsW))
		}
	}

	win := stLabel.Render("window ") + stText.Render(dur(time.Duration(span*float64(m.cfg.interval))))
	if held {
		win = stText.Render(idle) + stLabel.Render(" · last burst held")
	}
	legend := phaseGradient(phGen).at(0.8).Render("■") + stLabel.Render(" generating")
	for _, p := range []phase{phThinking, phAnswering} {
		if seen[p] {
			legend += " " + phaseStyle(p).Render("■") + stLabel.Render(" "+phaseName(p))
		}
	}
	legend += " " + phaseStyle(phPrompt).Render("■") + stLabel.Render(" prompt")
	return box(w, h, stBoxTokens, edge{left: []string{panelTitle("³", "tokens")}, right: []string{win}},
		edge{left: []string{legend}}, lines)
}

// ───────────────────────────── ⁴requests ─────────────────────────────

type reqCol struct {
	key   string
	title string
	w     int
	right bool
}

// requestCols picks the columns that fit: the narrow set always, then cached,
// ctx and prompt speed as the panel gets wider. "who" grows with spare room.
func requestCols(iw int) []reqCol {
	const base = 1 + 9 + 7 + 7 + 7 + 6 + 5 // mark, time, who, took, prompt, gen, t/s; each but the last with its gap
	spare := iw - base
	take := func(w int) bool {
		if spare < w+1 {
			return false
		}
		spare -= w + 1
		return true
	}
	cached, ctx, ptps := take(6), take(4), take(6)
	cols := []reqCol{
		{"time", "time", 8, false}, {"who", "who", 6 + min(6, max(0, spare)), false},
		{"took", "took", 6, true}, {"prompt", "prompt", 6, true},
	}
	if cached {
		cols = append(cols, reqCol{"cached", "cached", 6, true})
	}
	if ptps {
		cols = append(cols, reqCol{"ptps", "t/s", 6, true})
	}
	cols = append(cols, reqCol{"gen", "gen", 5, true}, reqCol{"gtps", "t/s", 5, true})
	if ctx {
		cols = append(cols, reqCol{"ctx", "ctx", 4, true})
	}
	return cols
}

type reqCell struct {
	text string
	st   lipgloss.Style
}

func shortDur(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < 100*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func reqLine(cols []reqCol, mark string, cells map[string]reqCell) string {
	var b strings.Builder
	b.WriteString(mark)
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(' ')
		}
		cell := cells[c.key]
		if c.right {
			b.WriteString(cell.st.Render(fitRight(cell.text, c.w)))
		} else {
			b.WriteString(cell.st.Render(fit(cell.text, c.w)))
		}
	}
	return b.String()
}

func pct(n, of int) string {
	if of <= 0 || n <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", float64(n)/float64(of)*100)
}

func who(name string) reqCell {
	if name == "" {
		return reqCell{"–", stDim}
	}
	return reqCell{name, stText}
}

// doneRow formats a finished request.
func (m *model) doneRow(r Request) (string, map[string]reqCell) {
	mark := " "
	switch {
	case r.Cancelled:
		mark = stErr.Render("×")
	case r.Truncated:
		mark = stWarn.Render("!")
	}
	if r.Cancelled && r.PromptMs == 0 && r.GenMs == 0 {
		// Cut short before the server logged any count: nothing is known but
		// when, who and how long.
		return mark, map[string]reqCell{
			"time": {r.Start.Format("15:04:05"), stLabel},
			"who":  who(r.Who),
			"took": {shortDur(r.End.Sub(r.Start)), stText},
			"gen":  {"cut", stErr},
		}
	}
	return mark, map[string]reqCell{
		"time":   {r.Start.Format("15:04:05"), stLabel},
		"who":    who(r.Who),
		"took":   {shortDur(r.End.Sub(r.Start)), stText},
		"prompt": {tokens(r.PromptTokens), stText},
		"cached": {tokens(r.Cached()), stLabel},
		"ptps":   {rate(r.PromptTPS), phaseStyle(phPrompt)},
		"gen":    {tokens(r.GenTokens), stText},
		"gtps":   {rate(r.GenTPS), phaseStyle(phGen)},
		"ctx":    {pct(r.CtxTokens, m.nCtx(r.Slot)), stLabel},
	}
}

// liveRow formats the request a slot is working on, from the slot's counters.
func (m *model) liveRow(s Slot) (string, map[string]reqCell) {
	ph := slotPhase(s)
	tr := m.trk.slots[s.ID]
	start := m.slotStart(s)
	cells := map[string]reqCell{
		"who":    who(m.liveCallers()[s.ID]),
		"prompt": {tokens(s.PromptProcessed), stTitle},
		"cached": {tokens(s.PromptCached), stLabel},
		"gen":    {tokens(s.Decoded()), stTitle},
		"ctx":    {pct(s.CtxTokens, s.NCtx), stLabel},
	}
	if !start.IsZero() {
		cells["time"] = reqCell{start.Format("15:04:05"), stTitle}
		cells["took"] = reqCell{shortDur(m.now.Sub(start)), stTitle}
	}
	if tr != nil {
		if ph == phPrompt && tr.promptRate > 0 {
			cells["ptps"] = reqCell{rate(tr.promptRate), phaseStyle(phPrompt)}
		} else if tr.promptAvg > 0 {
			cells["ptps"] = reqCell{rate(tr.promptAvg), phaseStyle(phPrompt)}
		}
		if tr.genRate > 0 {
			cells["gtps"] = reqCell{rate(tr.genRate), phaseStyle(ph)}
		}
	}
	return phaseStyle(ph).Render("▶"), cells
}

func (m *model) requestsBox(w, h int) string {
	iw, ih := w-2, h-2
	cols := requestCols(iw)
	head := map[string]reqCell{}
	for _, c := range cols {
		head[c.key] = reqCell{c.title, stTitle}
	}
	lines := []string{reqLine(cols, " ", head)}

	busy := append([]Slot(nil), m.slots...)
	sort.Slice(busy, func(a, b int) bool { return busy[a].ID < busy[b].ID })
	for _, s := range busy {
		if s.Processing && len(lines) < ih {
			mark, cells := m.liveRow(s)
			lines = append(lines, reqLine(cols, mark, cells))
		}
	}

	// Finished requests, newest first, with a line where the day changes.
	room := ih - len(lines)
	total := len(m.reqs)
	day := m.now.YearDay()
	for i := total - 1; i >= 0; i-- {
		if d := m.reqs[i].Start.YearDay(); d != day {
			total, day = total+1, d
		}
	}
	m.scrollMax = max(0, total-room)
	skip := min(m.scroll, m.scrollMax)
	day = m.now.YearDay()
	shown := 0
	for i := len(m.reqs) - 1; i >= 0 && shown < room; i-- {
		r := m.reqs[i]
		if d := r.Start.YearDay(); d != day {
			day = d
			if skip > 0 {
				skip--
			} else {
				lines = append(lines, stDim.Render(center("── "+r.Start.Format("Mon 2 Jan")+" ──", iw)))
				shown++
			}
		}
		if skip > 0 {
			skip--
			continue
		}
		if shown < room {
			mark, cells := m.doneRow(r)
			lines = append(lines, reqLine(cols, mark, cells))
			shown++
		}
	}
	if len(m.reqs) == 0 {
		// No history: say why, in full. The journal's own reason comes first.
		note, st := m.noRequests(), stDim
		if m.journalNote != "" {
			note, st = m.journalNote, stWarn
		}
		lines = append(lines, "")
		for _, l := range strings.Split(ansi.Wordwrap(note, iw-2, ""), "\n") {
			lines = append(lines, " "+st.Render(l))
		}
	}

	top := edge{left: []string{panelTitle("⁴", "requests")}}
	switch {
	case m.scroll > 0:
		top.right = []string{stText.Render(fmt.Sprintf("%d", min(m.scroll, m.scrollMax))) + stLabel.Render(" newer above") + stHi.Render(" ↑")}
	case m.scrollMax > 0:
		top.right = []string{stHi.Render("↑↓") + stLabel.Render(" scroll")}
	}
	var bottom edge
	if t := m.todayTotals(); t.n > 0 {
		bottom.left = []string{stLabel.Render("today ") + stText.Render(commas(t.n)) + stLabel.Render(" requests") + sep() +
			stText.Render(tokens(t.prompt)) + stLabel.Render(" prompt") + sep() + stText.Render(tokens(t.gen)) + stLabel.Render(" gen")}
	}
	return box(w, h, stBoxRequests, top, bottom, lines)
}

// ───────────────────────────── ⁵gpu ─────────────────────────────

func gpuName(g GPU) string {
	return strings.TrimPrefix(strings.TrimPrefix(g.Name, "NVIDIA "), "GeForce ")
}

func orDash(v float64, format string) string {
	if v < 0 {
		return "–"
	}
	return fmt.Sprintf(format, v)
}

func (m *model) gpuBox(w, h int) string {
	iw, ih := w-2, h-2
	top := edge{left: []string{panelTitle("⁵", "gpu")}}
	if len(m.gpus) == 0 {
		note := "waiting for NVML"
		if m.gpuNote != "" {
			note = m.gpuNote
		}
		return box(w, h, stBoxGPU, top, edge{}, []string{" " + stDim.Render(note)})
	}
	top.right = []string{stLabel.Render("driver ") + stText.Render(m.gpus[0].Driver)}

	mw := iw - 1 - 5 - 1 - 11 - 1 // meter width, the same for every row
	rows := func(g GPU) (name, util, mem, misc string) {
		name = " " + stTitle.Render(gpuName(g))
		if len(m.gpus) > 1 {
			name = " " + stLabel.Render(strconv.Itoa(g.Index)+" ") + stTitle.Render(gpuName(g))
		}
		uf := max(g.Util, 0) / 100
		util = " " + stLabel.Render("util ") + meter(mw, uf, gradUtil) + " " +
			gradUtil.at(uf).Render(fitRight(orDash(g.Util, "%.0f%%"), 11))
		mf := 0.0
		if g.MemTotal > 0 {
			mf = max(g.MemUsed, 0) / g.MemTotal
		}
		mem = " " + stLabel.Render("mem  ") + meter(mw, mf, gradMem) + " " +
			stText.Render(fitRight(fmt.Sprintf("%.1f/%.1fG", g.MemUsed/1024, g.MemTotal/1024), 11))
		power := orDash(g.Power, "%.0f W")
		if g.PowerCap > 0 {
			power = fmt.Sprintf("%.0f/%.0f W", g.Power, g.PowerCap)
		}
		misc = " " + stLabel.Render("temp ") + gradTemp.at((g.Temp-30)/60).Render(orDash(g.Temp, "%.0f°C")) +
			"  " + kv("power", power) + "  " + kv("fan", orDash(g.Fan, "%.0f%%"))
		return
	}

	var lines []string
	if len(m.gpus) == 1 {
		g := m.gpus[0]
		name, util, mem, misc := rows(g)
		lines = append(lines, name, util)
		if gh := ih - 4; gh >= 1 {
			hist := m.gpuHist[g.Index]
			vals := make([]float64, 2*iw)
			copy(vals[max(0, len(vals)-len(hist)):], hist[max(0, len(hist)-len(vals)):])
			lines = append(lines, brailleGraph(vals, iw, gh, false, func(int) (gradient, int) { return gradUtil, 0 })...)
		}
		lines = append(lines, mem, misc)
	} else {
		for _, g := range m.gpus {
			name, util, mem, misc := rows(g)
			lines = append(lines, name, util, mem, misc)
		}
	}
	return box(w, h, stBoxGPU, top, edge{}, lines)
}
