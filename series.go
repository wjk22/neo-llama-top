package main

import (
	"strings"
	"time"
)

// phase says what a slot, or one tick of the graph, is doing.
//
// GET /slots does not say whether generated tokens are reasoning or answer, so
// a generating slot is phGen unless its generation prompt shows that thinking
// is switched off, in which case everything it writes is the answer.
// phThinking is never produced from /slots; it is there for a source that can
// see the response stream.
type phase uint8

const (
	phIdle phase = iota
	phPrompt
	phGen
	phThinking
	phAnswering
)

// genPhase classifies generation from the prompt the server appends for the
// assistant turn. Templates that switch thinking off close the think block
// there ("<think>\n\n</think>\n\n"); then nothing generated is reasoning.
func genPhase(generationPrompt string) phase {
	if strings.HasSuffix(strings.TrimSpace(generationPrompt), "</think>") {
		return phAnswering
	}
	return phGen
}

// slotPhase is the state of one slot as far as /slots shows it.
func slotPhase(s Slot) phase {
	switch {
	case !s.Processing:
		return phIdle
	case s.Decoded() == 0:
		return phPrompt
	}
	return genPhase(s.Params.GenerationPrompt)
}

// sample is the throughput of one poll interval, summed over all slots.
type sample struct {
	gen    float64 // generated tokens per second
	prompt float64 // prompt tokens per second
	ph     phase
}

// series is a ring of samples, one per poll.
type series struct {
	buf  []sample
	head int // next write position
	n    int
}

func newSeries(capacity int) *series {
	return &series{buf: make([]sample, capacity)}
}

func (s *series) push(v sample) {
	s.buf[s.head] = v
	s.head = (s.head + 1) % len(s.buf)
	if s.n < len(s.buf) {
		s.n++
	}
}

// back returns the i-th newest sample, or nil when there is none.
func (s *series) back(i int) *sample {
	if i < 0 || i >= s.n {
		return nil
	}
	return &s.buf[(s.head-1-i+2*len(s.buf))%len(s.buf)]
}

// columns pools a window of span ticks into cols columns, oldest first, taking
// the highest sample of each column so a short burst stays visible when the
// window is wider than the graph. skip leaves out that many newest ticks.
func (s *series) columns(cols int, span float64, skip int) (gen, prompt []float64, ph []phase) {
	gen, prompt, ph = make([]float64, cols), make([]float64, cols), make([]phase, cols)
	k := span / float64(cols)
	for j := range cols {
		age := cols - 1 - j
		lo, hi := int(float64(age)*k), int(float64(age+1)*k)
		if hi <= lo {
			hi = lo + 1
		}
		for i := lo; i < hi; i++ {
			v := s.back(skip + i)
			if v == nil {
				break
			}
			if v.gen > gen[j] || ph[j] == phIdle {
				ph[j] = v.ph
			}
			gen[j] = max(gen[j], v.gen)
			prompt[j] = max(prompt[j], v.prompt)
		}
	}
	return gen, prompt, ph
}

const (
	seriesCap = 16384 // ticks kept
	idleCap   = 8192  // idle ticks kept at the end; more would only push the last burst out
)

// slotTrack remembers the previous poll of one slot.
type slotTrack struct {
	task       int
	processing bool
	processed  int
	decoded    int
	ctx        int
	at         time.Time
	firstSeen  time.Time // when the current task was first seen processing

	// The prompt counter moves one batch at a time, seconds apart. While a
	// batch is being evaluated the graph shows the previous batch's rate; when
	// the batch lands, those ticks are corrected to its real rate.
	lastChange  time.Time
	pending     int
	provisional float64

	genRate    float64 // smoothed, for display
	promptRate float64 // rate of the newest batch
	promptAvg  float64 // rate over the whole prompt so far
}

// tracker turns successive /slots readings into throughput.
type tracker struct {
	slots      map[int]*slotTrack
	ser        *series
	idleTicks  int       // idle samples at the end of the series
	lastActive time.Time // zero until activity was seen
}

func newTracker() *tracker {
	return &tracker{slots: map[int]*slotTrack{}, ser: newSeries(seriesCap)}
}

// ingest takes one /slots reading and appends one sample.
func (t *tracker) ingest(slots []Slot, at time.Time) {
	var smp sample
	active := false
	seen := map[int]bool{}
	for _, s := range slots {
		seen[s.ID] = true
		tr := t.slots[s.ID]
		if tr == nil {
			// First sight: no earlier reading to diff against.
			tr = &slotTrack{firstSeen: at, lastChange: at}
			t.slots[s.ID] = tr
			tr.remember(s, at)
			active = active || s.Processing
			continue
		}
		dt := at.Sub(tr.at).Seconds()
		if dt <= 0 {
			dt = 0.5
		}
		var dGen, dPrompt int
		switch {
		case s.Processing && tr.processing && s.Task == tr.task:
			dGen = s.Decoded() - tr.decoded
			dPrompt = s.PromptProcessed - tr.processed
		case s.Processing:
			// A new task started since the last reading; its counters began at zero.
			dGen, dPrompt = s.Decoded(), s.PromptProcessed
			tr.firstSeen, tr.lastChange = tr.at, tr.at
			tr.pending, tr.provisional = 0, 0
			tr.genRate, tr.promptRate, tr.promptAvg = 0, 0, 0
		case tr.processing && s.Task == tr.task && tr.decoded > 0:
			// Finished since the last reading. The counters are back at zero,
			// but the context grew by the tokens generated in between.
			dGen = s.CtxTokens - tr.ctx
		}
		dGen, dPrompt = max(0, dGen), max(0, dPrompt)

		if dGen > 0 {
			r := float64(dGen) / dt
			smp.gen += r
			smp.ph = genPhase(s.Params.GenerationPrompt)
			if s.Processing {
				if tr.genRate == 0 {
					tr.genRate = r
				} else {
					tr.genRate = 0.6*tr.genRate + 0.4*r
				}
			}
		}
		switch {
		case dPrompt > 0:
			el := at.Sub(tr.lastChange).Seconds()
			if el <= 0 {
				el = dt
			}
			r := float64(dPrompt) / el
			for i := range tr.pending {
				if p := t.ser.back(i); p != nil {
					p.prompt = max(0, p.prompt+r-tr.provisional)
				}
			}
			smp.prompt += r
			tr.provisional, tr.promptRate = r, r
			tr.pending, tr.lastChange = 0, at
			if d := at.Sub(tr.firstSeen).Seconds(); d > 0 {
				tr.promptAvg = float64(s.PromptProcessed) / d
			}
		case s.Processing && s.Decoded() == 0:
			tr.pending++
			smp.prompt += tr.provisional
		}
		if !s.Processing {
			tr.genRate, tr.promptRate, tr.promptAvg = 0, 0, 0
		}
		if s.Processing || dGen > 0 || dPrompt > 0 {
			active = true
		}
		tr.remember(s, at)
	}
	for id := range t.slots {
		if !seen[id] {
			delete(t.slots, id)
		}
	}
	if active && smp.ph == phIdle {
		smp.ph = phPrompt
	}
	t.add(smp, active, at)
}

// miss records a poll that got no slots: nothing can be diffed across it.
func (t *tracker) miss(at time.Time) {
	t.slots = map[int]*slotTrack{}
	t.add(sample{}, false, at)
}

func (t *tracker) add(smp sample, active bool, at time.Time) {
	if active {
		t.idleTicks = 0
		t.lastActive = at
		t.ser.push(smp)
		return
	}
	if t.idleTicks < idleCap {
		t.idleTicks++
		t.ser.push(smp)
	}
}

func (tr *slotTrack) remember(s Slot, at time.Time) {
	tr.task, tr.processing = s.Task, s.Processing
	tr.processed, tr.decoded, tr.ctx = s.PromptProcessed, s.Decoded(), s.CtxTokens
	tr.at = at
}

// window decides which part of the series a graph of cols columns shows.
//
// While tokens flow, one column is one poll. When the server goes idle the
// window stretches instead of scrolling the last burst away: up to three times
// its normal span. After that the picture is held, with the burst in the left
// fifth of the graph, and only the idle time shown next to it keeps counting.
//
// idleCols is how many columns at the right end are idle time.
func (t *tracker) window(cols int) (span float64, skip int, held bool, idleCols int) {
	keep := cols * 3 / 5 // ticks of activity that stay in view
	maxSpan := 3 * cols
	eff := min(t.idleTicks, maxSpan-keep)
	span = float64(max(cols, eff+keep))
	return span, t.idleTicks - eff, t.idleTicks > eff, int(float64(eff) * float64(cols) / span)
}
