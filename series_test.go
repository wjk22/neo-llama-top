package main

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type reading struct {
	at   time.Time
	slot Slot
}

// longPrompt loads the /slots counters recorded ten times a second while the
// server evaluated a 32,518-token prompt and then generated 105 tokens.
func longPrompt(t *testing.T) []reading {
	t.Helper()
	f, err := os.Open("testdata/slots_long_prompt.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base := time.Date(2026, 11, 27, 8, 51, 13, 0, time.UTC)
	var out []reading
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		c := strings.Split(sc.Text(), "\t")
		sec, _ := strconv.ParseFloat(c[0], 64)
		n := func(i int) int { v, _ := strconv.Atoi(c[i]); return v }
		s := Slot{ID: 0, NCtx: 131072, Processing: c[1] == "1", Task: n(2), CtxTokens: n(3), PromptProcessed: n(4)}
		s.NextToken = []struct {
			NRemain  int `json:"n_remain"`
			NDecoded int `json:"n_decoded"`
		}{{NDecoded: n(5)}}
		s.Params.GenerationPrompt = "<|im_start|>assistant\n<think>\n"
		out = append(out, reading{base.Add(time.Duration(sec * float64(time.Second))), s})
	}
	return out
}

// replay feeds every step-th reading to a tracker and integrates the rates
// back into token counts.
func replay(rs []reading, step int) (tr *tracker, promptTokens, genTokens float64) {
	tr = newTracker()
	var prev time.Time
	var dts []float64
	for i := 0; i < len(rs); i += step {
		tr.ingest([]Slot{rs[i].slot}, rs[i].at)
		if !prev.IsZero() {
			dts = append(dts, rs[i].at.Sub(prev).Seconds())
		} else {
			dts = append(dts, 0)
		}
		prev = rs[i].at
	}
	// Sample i (oldest first) covers the interval that ended at reading i.
	n := len(dts)
	for i := range n {
		s := tr.ser.back(n - 1 - i)
		promptTokens += s.prompt * dts[i]
		genTokens += s.gen * dts[i]
	}
	return tr, promptTokens, genTokens
}

// Throughput comes from diffing the slot counters. Integrated over the
// request it must give back the tokens the server reported: 32,518 and 105.
func TestTrackerConservesTokens(t *testing.T) {
	rs := longPrompt(t)
	for _, step := range []int{1, 5, 10} { // polling every 0.1 s, 0.5 s and 1 s
		_, prompt, gen := replay(rs, step)
		if math.Abs(prompt-32518)/32518 > 0.03 {
			t.Errorf("every %d00 ms: prompt tokens from the graph = %.0f, want 32518 ± 3%%", step, prompt)
		}
		if math.Abs(gen-105) > 6 {
			t.Errorf("every %d00 ms: generated tokens from the graph = %.1f, want 105 ± 6", step, gen)
		}
	}
}

// The prompt counter moves a batch at a time, several polls apart. The graph
// must not show a spike per batch with nothing in between.
func TestTrackerSpreadsPromptBatches(t *testing.T) {
	rs := longPrompt(t)
	tr, _, _ := replay(rs, 5)
	var rates []float64
	for i := tr.ser.n - 1; i >= 0; i-- {
		if s := tr.ser.back(i); s.prompt > 0 {
			rates = append(rates, s.prompt)
		}
	}
	if len(rates) < 40 {
		t.Fatalf("only %d ticks show prompt throughput; the prompt took 25 s at 2 ticks per second", len(rates))
	}
	// The server logged 1,274 t/s for the whole prompt and up to 1,463 t/s per
	// batch. A batch is timed to the poll that saw it land, so a short batch
	// can read up to half a second too fast; an unspread 4096-token batch
	// would read 8,192 t/s.
	for _, r := range rates {
		if r > 2500 {
			t.Errorf("prompt rate %.0f t/s: a batch was not spread over the time it took", r)
		}
	}
}

func TestTrackerPhasesAndIdle(t *testing.T) {
	rs := longPrompt(t)
	tr, _, _ := replay(rs, 5)
	// The recording ends two readings (at this step) after the slot was released.
	if tr.idleTicks < 1 || tr.idleTicks > 3 {
		t.Errorf("idle ticks at the end = %d", tr.idleTicks)
	}
	sawPrompt, sawGen := false, false
	for i := range tr.ser.n {
		switch s := tr.ser.back(i); s.ph {
		case phPrompt:
			sawPrompt = true
		case phGen:
			sawGen = true
		case phThinking, phAnswering:
			t.Errorf("phase %v: /slots cannot tell thinking from answering while thinking is on", s.ph)
		}
	}
	if !sawPrompt || !sawGen {
		t.Errorf("phases seen: prompt %v, gen %v", sawPrompt, sawGen)
	}
	if tr.lastActive.IsZero() {
		t.Error("lastActive not set")
	}

	// With thinking switched off for the request, generation is the answer.
	tr = newTracker()
	for i := 0; i < len(rs); i += 5 {
		s := rs[i].slot
		s.Params.GenerationPrompt = "<|im_start|>assistant\n<think>\n\n</think>\n\n"
		tr.ingest([]Slot{s}, rs[i].at)
	}
	saw := false
	for i := range tr.ser.n {
		if s := tr.ser.back(i); s.gen > 0 {
			saw = true
			if s.ph != phAnswering {
				t.Errorf("thinking off: phase %v, want answering", s.ph)
			}
		}
	}
	if !saw {
		t.Error("no generation seen")
	}
}

func TestTrackerMiss(t *testing.T) {
	rs := longPrompt(t)
	tr := newTracker()
	tr.ingest([]Slot{rs[100].slot}, rs[100].at)
	tr.miss(rs[105].at)
	tr.ingest([]Slot{rs[110].slot}, rs[110].at)
	// Nothing can be diffed across a failed poll: no made-up rate.
	for i := range tr.ser.n {
		if s := tr.ser.back(i); s.gen != 0 || s.prompt != 0 {
			t.Errorf("sample %d after a missed poll: %+v", i, *s)
		}
	}
}

func TestSeriesColumns(t *testing.T) {
	s := newSeries(8)
	for i := 1; i <= 10; i++ { // the ring keeps the newest 8: 3..10
		s.push(sample{gen: float64(i), ph: phGen})
	}
	if s.n != 8 || s.back(0).gen != 10 || s.back(7).gen != 3 || s.back(8) != nil {
		t.Fatalf("ring: n=%d newest=%v", s.n, s.back(0))
	}
	eq := func(name string, got, want []float64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", name, got, want)
			}
		}
	}
	gen, _, ph := s.columns(4, 4, 0)
	eq("one tick per column", gen, []float64{7, 8, 9, 10})
	if ph[3] != phGen {
		t.Errorf("phase = %v", ph[3])
	}
	gen, _, _ = s.columns(4, 8, 0)
	eq("two ticks per column keep the higher", gen, []float64{4, 6, 8, 10})
	gen, _, _ = s.columns(4, 4, 2)
	eq("skipping the newest two", gen, []float64{5, 6, 7, 8})
	gen, _, _ = s.columns(4, 16, 0)
	eq("window longer than the history", gen, []float64{0, 0, 6, 10})
}

// The window of a 100-column graph: one column per poll while busy, stretching
// when idle, held once it is three times as long.
func TestWindowStretchesWhenIdle(t *testing.T) {
	tr := newTracker()
	for _, c := range []struct {
		idle     int
		span     float64
		skip     int
		held     bool
		idleCols int
	}{
		{0, 100, 0, false, 0},       // busy: 100 ticks in 100 columns
		{30, 100, 0, false, 30},     // idle for a while: still scrolling
		{40, 100, 0, false, 40},     // the last 60 ticks of activity are about to leave
		{100, 160, 0, false, 62},    // stretched: they stay, compressed
		{240, 300, 0, false, 80},    // stretched to three times
		{241, 300, 1, true, 80},     // held from here on
		{5000, 300, 4760, true, 80}, // however long the server idles
	} {
		tr.idleTicks = c.idle
		span, skip, held, idleCols := tr.window(100)
		if span != c.span || skip != c.skip || held != c.held || idleCols != c.idleCols {
			t.Errorf("idle %d ticks: span %v skip %d held %v idleCols %d, want %v %d %v %d",
				c.idle, span, skip, held, idleCols, c.span, c.skip, c.held, c.idleCols)
		}
	}
}

// After a long idle time the last burst must still be in the picture.
func TestLastBurstStaysVisible(t *testing.T) {
	tr := newTracker()
	at := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)
	slot := func(processing bool, decoded int) []Slot {
		s := Slot{ID: 0, NCtx: 1000, Processing: processing, Task: 1, CtxTokens: 10 + decoded}
		s.NextToken = []struct {
			NRemain  int `json:"n_remain"`
			NDecoded int `json:"n_decoded"`
		}{{NDecoded: decoded}}
		return []Slot{s}
	}
	step := func(s []Slot) { at = at.Add(500 * time.Millisecond); tr.ingest(s, at) }

	step(slot(false, 0))
	for i := 1; i <= 20; i++ { // a burst of 10 seconds at 50 t/s
		step(slot(true, 25*i))
	}
	for range 7 * 3600 * 2 { // then seven hours of nothing
		step(slot(false, 0))
	}

	const cols = 120
	span, skip, held, _ := tr.window(cols)
	gen, _, _ := tr.ser.columns(cols, span, skip)
	lit := 0
	for _, v := range gen {
		if v > 0 {
			lit++
			if math.Abs(v-50) > 1 {
				t.Errorf("burst drawn at %.1f t/s, want 50", v)
			}
		}
	}
	if !held || lit < 5 {
		t.Errorf("after 7 h idle: held=%v, burst covers %d of %d columns", held, lit, cols)
	}
	if got := at.Sub(tr.lastActive); got != 7*time.Hour {
		t.Errorf("idle for %v, want 7h", got)
	}
}
