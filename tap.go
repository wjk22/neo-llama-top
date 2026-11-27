package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

// The caller of a request comes from nltap's log. nltap knows who called and
// what llama-server answered; the journal knows the request by its task
// number. The two are joined on the timings both carry.

type tapMsg struct {
	events []tap.Event
}

const tapBacklog = 4 << 20 // at start, read this much of the end of the log

// followTap reads nltap's log until ctx ends: its end at start, then whatever
// is appended. It follows the file by name, so a log that nltap moved aside
// and started anew is picked up. A missing log is fine; then nobody is named.
func followTap(ctx context.Context, path string, interval time.Duration, send func(tapMsg)) {
	var f *os.File
	var pending []byte // a line whose end has not been written yet
	first := true
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if f == nil {
			if h, err := os.Open(path); err == nil {
				f, pending = h, nil
				if st, err := f.Stat(); first && err == nil && st.Size() > tapBacklog {
					f.Seek(st.Size()-tapBacklog, io.SeekStart)
					pending = []byte{0} // marks the cut line at the seek point, which is dropped
				}
				first = false
			}
		}
		if f != nil {
			data, _ := io.ReadAll(f)
			pending = append(pending, data...)
			var evs []tap.Event
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				var e tap.Event
				if json.Unmarshal(pending[:i], &e) == nil && e.Ev != "" {
					evs = append(evs, e)
				}
				pending = pending[i+1:]
			}
			if len(evs) > 0 {
				send(tapMsg{events: evs})
			}
			// Moved aside or deleted: what was read is the old file's end; open the new one.
			cur, err1 := os.Stat(path)
			mine, err2 := f.Stat()
			if err1 != nil || err2 != nil || !os.SameFile(cur, mine) {
				f.Close()
				f = nil
			}
		}
		select {
		case <-ctx.Done():
			if f != nil {
				f.Close()
			}
			return
		case <-tick.C:
		}
	}
}

const maxTapDone = 5000 // finished calls kept while their request is not in the journal yet

func (m *model) onTap(t tapMsg) {
	for _, e := range t.events {
		switch e.Ev {
		case tap.Up:
			m.tapLive = map[uint64]tap.Event{}
		case tap.Start:
			m.tapLive[e.ID] = e
		case tap.Done:
			delete(m.tapLive, e.ID)
			if e.Method != "POST" || e.Error != "" {
				continue // never reached a slot
			}
			if !m.nameRequest(e) {
				m.tapDone = append(m.tapDone, e)
			}
		}
	}
	if n := len(m.tapDone); n > maxTapDone {
		m.tapDone = append([]tap.Event(nil), m.tapDone[n-maxTapDone:]...)
	}
}

// sameRequest reports whether a finished call and a journal request are the
// same request.
//
// With timings they are compared exactly: the journal prints the token counts
// and, to a hundredth of a millisecond, the two durations that llama-server
// also sent in its answer. A call that was cut short has no timings; it is
// matched to the request that was released when the caller hung up.
func sameRequest(e tap.Event, r Request) bool {
	if r.Start.Before(e.Start.Add(-time.Second)) {
		return false // launched before the call arrived
	}
	if tm, ok := e.ParseTimings(); ok {
		return tm.PromptN == r.PromptTokens && tm.PredictedN == r.GenTokens &&
			absF(tm.PromptMs-r.PromptMs) < 0.011 && absF(tm.PredictedMs-r.GenMs) < 0.011 &&
			r.End.Before(e.End.Add(5*time.Second))
	}
	// The server notices a caller that hung up when its current batch is done.
	return e.Aborted && r.End.After(e.End.Add(-time.Second)) && r.End.Before(e.End.Add(10*time.Second))
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// nameRequest writes the caller of a finished call into its request, if the
// journal has reported that request already.
func (m *model) nameRequest(e tap.Event) bool {
	for i := len(m.reqs) - 1; i >= 0 && i >= len(m.reqs)-500; i-- {
		if r := &m.reqs[i]; r.Who == "" && sameRequest(e, *r) {
			r.Who = e.Who
			return true
		}
	}
	return false
}

// nameFromTap looks for the call behind a request the journal just reported.
func (m *model) nameFromTap(r *Request) {
	for i := len(m.tapDone) - 1; i >= 0; i-- {
		if sameRequest(m.tapDone[i], *r) {
			r.Who = m.tapDone[i].Who
			m.tapDone = append(m.tapDone[:i], m.tapDone[i+1:]...)
			return
		}
	}
}

// liveCallers names the caller of each busy slot.
//
// llama-server starts requests in the order they arrive, so the oldest call
// still in flight belongs to the request that was launched first. A slot
// whose request was launched before any open call arrived was not called
// through nltap and stays unnamed.
func (m *model) liveCallers() map[int]string {
	out := map[int]string{}
	if len(m.tapLive) == 0 {
		return out
	}
	calls := make([]tap.Event, 0, len(m.tapLive))
	for _, e := range m.tapLive {
		calls = append(calls, e)
	}
	sort.Slice(calls, func(a, b int) bool { return calls[a].Start.Before(calls[b].Start) })

	type running struct {
		slot  int
		start time.Time
	}
	var runs []running
	for _, s := range m.slots {
		if s.Processing {
			if start := m.slotStart(s); !start.IsZero() {
				runs = append(runs, running{s.ID, start})
			}
		}
	}
	sort.Slice(runs, func(a, b int) bool { return runs[a].start.Before(runs[b].start) })

	// A start seen on the slot counters, not in the journal, is known only to
	// the poll before it; allow for that.
	slack := m.cfg.interval + time.Second
	j := 0
	for _, r := range runs {
		if j < len(calls) && !calls[j].Start.After(r.start.Add(slack)) {
			out[r.slot] = calls[j].Who
			j++
		}
	}
	return out
}

// slotStart is when the slot's current request started: its launch line in the
// journal, or else the poll that first saw it.
func (m *model) slotStart(s Slot) time.Time {
	if lv := m.live[s.ID]; lv != nil && lv.Task == s.Task {
		return lv.Start
	}
	if tr := m.trk.slots[s.ID]; tr != nil {
		return tr.firstSeen
	}
	return time.Time{}
}
