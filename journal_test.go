package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// parseFixture runs the captured journal through the parser and returns the
// events in order.
func parseFixture(t *testing.T) []journalEvent {
	t.Helper()
	f, err := os.Open("testdata/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := newJournalParser()
	var evs []journalEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e journalEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if ev, ok := p.parse(e.time(), e.PID, e.text()); ok {
			evs = append(evs, ev)
		}
	}
	return evs
}

func done(evs []journalEvent) []Request {
	var out []Request
	for _, ev := range evs {
		if ev.req != nil && ev.req.Done {
			out = append(out, *ev.req)
		}
	}
	return out
}

func TestJournalRequests(t *testing.T) {
	evs := parseFixture(t)
	reqs := done(evs)
	if len(reqs) != 4 {
		t.Fatalf("%d finished requests, want 4", len(reqs))
	}

	// A follow-up in a long conversation: nearly all of the prompt was cached.
	r := reqs[0]
	if r.Task != 22278 || r.PromptTokens != 118 || r.GenTokens != 92 || r.CtxTokens != 57531 || r.Truncated || r.Cancelled {
		t.Errorf("request 0: %+v", r)
	}
	if r.PromptTPS != 89.94 || r.GenTPS != 42.64 || r.PromptMs != 1311.93 || r.GenMs != 2134.27 {
		t.Errorf("request 0 timings: %+v", r)
	}
	if got := r.Cached(); got != 57531-118-92+1 {
		t.Errorf("request 0 cached = %d", got)
	}
	if got := r.End.Sub(r.Start).Round(10 * time.Millisecond); got != 3390*time.Millisecond {
		t.Errorf("request 0 took %v", got)
	}

	// Three fresh prompts: nothing cached.
	for i, want := range []struct{ task, prompt, gen, ctx int }{
		{22784, 30, 700, 729}, {23617, 20, 10, 29}, {23649, 32518, 105, 32622},
	} {
		r := reqs[i+1]
		if r.Task != want.task || r.PromptTokens != want.prompt || r.GenTokens != want.gen || r.CtxTokens != want.ctx {
			t.Errorf("request %d: %+v", i+1, r)
		}
		if r.Cached() != 0 {
			t.Errorf("request %d cached = %d, want 0", i+1, r.Cached())
		}
	}

	// The long prompt logs its progress; the total can be estimated from it.
	var progress []Request
	for _, ev := range evs {
		if ev.req != nil && !ev.req.Done && ev.req.Task == 23649 && ev.req.PromptSeen > 0 {
			progress = append(progress, *ev.req)
		}
	}
	if len(progress) != 8 {
		t.Fatalf("%d progress events for the long prompt, want 8", len(progress))
	}
	if p := progress[2]; p.PromptSeen != 16384 || p.PromptProgress != 0.50 || p.PromptTotal(0) != 32768 {
		t.Errorf("progress: seen %d, fraction %v, total %d", p.PromptSeen, p.PromptProgress, p.PromptTotal(0))
	}
	// The logged fraction includes cached tokens: 28,672 evaluated at 0.30 with
	// 3,871 cached means about 104.6k to evaluate, not 95.6k.
	if got := (Request{PromptSeen: 28672, PromptProgress: 0.30}).PromptTotal(3871); got != 104606 {
		t.Errorf("total with cached tokens = %d", got)
	}

	// The log prefix is the time since the server started.
	var start time.Time
	for _, ev := range evs {
		if !ev.procStart.IsZero() {
			start = ev.procStart
		}
	}
	if want := time.Date(2026, 11, 27, 0, 16, 28, 0, time.UTC); absDur(start.Sub(want)) > 2*time.Second {
		t.Errorf("server start = %v, want about %v", start.UTC(), want)
	}
}

func TestJournalRestartAndCancel(t *testing.T) {
	p := newJournalParser()
	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	feed := func(pid, msg string) (journalEvent, bool) { return p.parse(t0, pid, msg) }

	feed("100", "0.05.000.000 I slot launch_slot_: id  0 | task 7 | processing task, is_child = 0")
	feed("100", "0.06.000.000 W srv          stop: cancel task, id_task = 7")
	ev, ok := feed("100", "0.06.100.000 I slot      release: id  0 | task 7 | stop processing: n_tokens = 40, truncated = 0")
	if !ok || ev.req == nil || !ev.req.Done || !ev.req.Cancelled {
		t.Errorf("cancelled request: %+v", ev.req)
	}
	// It logged no token counts, so nothing can be said about its cache use.
	if got := ev.req.Cached(); got != 0 {
		t.Errorf("cancelled request: cached = %d, want 0", got)
	}

	feed("100", "0.07.000.000 I slot launch_slot_: id  0 | task 9 | processing task, is_child = 0")
	ev, ok = feed("200", "0.00.100.000 I srv  llama_server: initializing ...")
	if !ok || !ev.reset {
		t.Errorf("a new server process must reset the requests in flight: %+v", ev)
	}
	if len(p.live) != 0 {
		t.Errorf("%d requests still in flight after the restart", len(p.live))
	}

	// Lines that are not from llama-server's logger change nothing.
	if _, ok := feed("200", "Started llama-server.service - llama.cpp inference server."); ok {
		t.Error("systemd line reported as an event")
	}
}

// followJournal is run against a stand-in for journalctl that prints the
// captured lines. Several of them share one timestamp, as in the real journal.
func TestFollowJournal(t *testing.T) {
	dir := t.TempDir()
	fixture, err := filepath.Abs("testdata/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + fixture + "'\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgs := make(chan journalMsg, 64)
	go followJournal(ctx, "llama-server", func(m journalMsg) { msgs <- m })

	var reqs []Request
	deadline := time.After(5 * time.Second)
	for len(reqs) < 4 {
		select {
		case m := <-msgs:
			reqs = append(reqs, done(m.events)...)
		case <-deadline:
			t.Fatalf("got %d finished requests, want 4", len(reqs))
		}
	}
	if r := reqs[3]; r.PromptTokens != 32518 || r.GenTokens != 105 || r.GenTPS != 49.02 {
		t.Errorf("last request: %+v", r)
	}
}

func TestFollowJournalMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	msgs := make(chan journalMsg, 4)
	go followJournal(context.Background(), "llama-server", func(m journalMsg) { msgs <- m })
	select {
	case m := <-msgs:
		if m.note == "" {
			t.Errorf("no note for a missing journalctl: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no message")
	}
}
