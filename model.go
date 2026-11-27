package main

import (
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

type config struct {
	url      string
	unit     string
	tapLog   string // nltap's log, which names the callers
	interval time.Duration
	version  string
}

// The five panels, in the order of their number keys.
const (
	pServer = iota
	pSlots
	pTokens
	pRequests
	pGPU
	panelCount
)

const maxRequests = 5000 // finished requests kept in memory

// totals sums the finished requests of one day.
type totals struct {
	day    string
	n      int
	prompt int
	gen    int
	busyMs float64
	genMs  float64
}

func dayOf(t time.Time) string { return t.Format("2006-01-02") }

func (t *totals) add(r Request) {
	if d := dayOf(r.End); d != t.day {
		*t = totals{day: d}
	}
	t.n++
	t.prompt += r.PromptTokens
	t.gen += r.GenTokens
	t.busyMs += r.PromptMs + r.GenMs
	t.genMs += r.GenMs
}

type model struct {
	cfg  config
	w, h int
	show [panelCount]bool
	now  time.Time

	// llama-server, over HTTP
	health    healthState
	healthMsg string
	props     *Props
	meta      *ModelMeta
	metrics   map[string]float64
	latency   time.Duration
	slots     []Slot
	slotsErr  string
	trk       *tracker

	// request history, from the journal
	reqs        []Request        // finished, oldest first
	live        map[int]*Request // in flight, by slot
	procStart   time.Time        // start of the llama-server process
	journalNote string
	today       totals
	scroll      int // rows the request list is scrolled back
	scrollMax   int

	// callers, from nltap's log
	tapLive map[uint64]tap.Event // calls in flight, by nltap's request number
	tapDone []tap.Event          // finished calls whose request the journal has not reported yet

	// GPUs, from NVML
	gpus    []GPU
	gpuHist map[int][]float64 // utilisation history by GPU index
	gpuNote string
}

func newModel(cfg config) *model {
	m := &model{
		cfg:     cfg,
		now:     time.Now(),
		trk:     newTracker(),
		live:    map[int]*Request{},
		tapLive: map[uint64]tap.Event{},
		gpuHist: map[int][]float64{},
	}
	for i := range m.show {
		m.show[i] = true
	}
	return m
}

// tickMsg keeps the clock and the idle times moving when the server is slow
// to answer and no poll result arrives.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd { return tick() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height

	case tea.KeyPressMsg:
		switch k := msg.String(); k {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1", "2", "3", "4", "5":
			i := int(k[0] - '1')
			m.show[i] = !m.show[i]
		case "up", "k":
			m.scroll--
		case "down", "j":
			m.scroll++
		case "pgup":
			m.scroll -= 10
		case "pgdown":
			m.scroll += 10
		case "home":
			m.scroll = 0
		case "end":
			m.scroll = m.scrollMax
		}
		m.scroll = min(max(m.scroll, 0), m.scrollMax)

	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()

	case pollMsg:
		m.onPoll(msg)

	case journalMsg:
		m.onJournal(msg)

	case tapMsg:
		m.onTap(msg)

	case gpuMsg:
		m.onGPU(msg)
	}
	return m, nil
}

func (m *model) onPoll(p pollMsg) {
	m.now = p.at
	m.latency = p.latency
	if p.slotsErr != nil {
		m.slots, m.slotsErr = nil, shortNetErr(p.slotsErr)
		m.trk.miss(p.at)
	} else {
		m.slots, m.slotsErr = p.slots, ""
		m.trk.ingest(p.slots, p.at)
	}
	if p.hasHealth {
		m.health, m.healthMsg = p.health, p.healthMsg
	}
	if p.hasProps {
		m.props = p.props
		if p.meta != nil {
			m.meta = p.meta
		}
		if !p.props.EndpointMetrics {
			m.metrics = nil
		}
	}
	if p.hasMetrics {
		m.metrics = p.metrics
	}
}

func (m *model) onJournal(j journalMsg) {
	if j.note != "" {
		m.journalNote = j.note
	}
	for _, ev := range j.events {
		if ev.reset {
			m.live = map[int]*Request{}
		}
		if !ev.procStart.IsZero() {
			m.procStart = ev.procStart
		}
		r := ev.req
		if r == nil {
			continue
		}
		if !r.Done {
			m.live[r.Slot] = r
			continue
		}
		delete(m.live, r.Slot)
		m.nameFromTap(r)
		m.reqs = append(m.reqs, *r)
		m.today.add(*r)
		if m.scroll > 0 {
			m.scroll++ // keep the rows in place while the list is scrolled back
		}
	}
	if len(m.reqs) > maxRequests {
		m.reqs = append([]Request(nil), m.reqs[len(m.reqs)-maxRequests:]...)
	}
}

func (m *model) onGPU(g gpuMsg) {
	if g.note != "" {
		m.gpuNote = g.note
		m.gpus = nil
		return
	}
	m.gpuNote = ""
	found := false
	for i := range m.gpus {
		if m.gpus[i].Index == g.gpu.Index {
			m.gpus[i], found = g.gpu, true
		}
	}
	if !found {
		m.gpus = append(m.gpus, g.gpu)
		sort.Slice(m.gpus, func(a, b int) bool { return m.gpus[a].Index < m.gpus[b].Index })
	}
	h := append(m.gpuHist[g.gpu.Index], max(g.gpu.Util, 0)/100)
	if len(h) > 2048 {
		h = append([]float64(nil), h[len(h)-1024:]...)
	}
	m.gpuHist[g.gpu.Index] = h
}

// todayTotals returns the sums for the current day; zero after midnight until
// the first request of the new day.
func (m *model) todayTotals() totals {
	if m.today.day != dayOf(m.now) {
		return totals{}
	}
	return m.today
}

// lastRequest returns the newest finished request.
func (m *model) lastRequest() (Request, bool) {
	if len(m.reqs) == 0 {
		return Request{}, false
	}
	return m.reqs[len(m.reqs)-1], true
}

// idleSince is when the server last worked: seen on the slots while nltop
// runs, or else the end of the newest request in the journal.
func (m *model) idleSince() time.Time {
	t := m.trk.lastActive
	if r, ok := m.lastRequest(); ok && r.End.After(t) {
		t = r.End
	}
	return t
}

// busy reports whether any slot is processing.
func (m *model) busy() bool {
	for _, s := range m.slots {
		if s.Processing {
			return true
		}
	}
	return false
}

// nCtx is the context size of a slot, for requests that only know their slot.
func (m *model) nCtx(slot int) int {
	for _, s := range m.slots {
		if s.ID == slot {
			return s.NCtx
		}
	}
	if m.props != nil {
		return m.props.Defaults.NCtx
	}
	return 0
}
