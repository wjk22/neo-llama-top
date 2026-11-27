package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

// The timings llama-server sent in its answers to the three requests of the
// journal fixture, as recorded: full precision, where the journal prints two
// decimals (243.415 here, 243.41 there).
var answeredTimings = map[int]string{
	22784: `{"cache_n":0,"prompt_n":30,"prompt_ms":243.415,"prompt_per_second":123.246,"predicted_n":700,"predicted_ms":11509.488}`,
	23617: `{"cache_n":0,"prompt_n":20,"prompt_ms":198.18,"predicted_n":10,"predicted_ms":168.041}`,
	23649: `{"cache_n":0,"prompt_n":32518,"prompt_ms":25525.761,"predicted_n":105,"predicted_ms":2121.66}`,
}

// callFor is the line nltap would have logged for a journal request: the call
// arrives a little before the launch and ends a little after the release.
func callFor(r Request, id uint64, who string) tap.Event {
	return tap.Event{Ev: tap.Done, ID: id, Who: who, Method: "POST", Path: "/v1/chat/completions", Status: 200,
		Start: r.Start.Add(-20 * time.Millisecond), End: r.End.Add(5 * time.Millisecond),
		Timings: json.RawMessage(answeredTimings[r.Task])}
}

func whoOf(m *model) map[int]string {
	out := map[int]string{}
	for _, r := range m.reqs {
		out[r.Task] = r.Who
	}
	return out
}

// Callers are joined to journal requests by the timings, whichever of the two
// is reported first.
func TestCallersByTimings(t *testing.T) {
	reqs := done(parseFixture(t)) // tasks 22278 (not called through nltap), 22784, 23617, 23649
	calls := []tap.Event{callFor(reqs[1], 1, "Motoko"), callFor(reqs[2], 2, "Rico"), callFor(reqs[3], 3, "Motoko")}
	want := map[int]string{22278: "", 22784: "Motoko", 23617: "Rico", 23649: "Motoko"}

	for _, order := range []string{"journal first", "nltap first"} {
		m := newModel(config{interval: 500 * time.Millisecond})
		journal := func() { m.Update(journalMsg{events: parseFixture(t)}) }
		log := func() { m.Update(tapMsg{events: calls}) }
		if order == "journal first" {
			journal()
			log()
		} else {
			log()
			journal()
		}
		got := whoOf(m)
		for task, who := range want {
			if got[task] != who {
				t.Errorf("%s: task %d called by %q, want %q", order, task, got[task], who)
			}
		}
		if len(m.tapDone) != 0 {
			t.Errorf("%s: %d calls left unmatched", order, len(m.tapDone))
		}
	}

	// The same token counts are not enough: the durations must agree too.
	m := newModel(config{interval: 500 * time.Millisecond})
	other := callFor(reqs[2], 9, "Rico")
	other.Timings = json.RawMessage(`{"prompt_n":20,"prompt_ms":211.5,"predicted_n":10,"predicted_ms":170.2}`)
	m.Update(journalMsg{events: parseFixture(t)})
	m.Update(tapMsg{events: []tap.Event{other}})
	if who := whoOf(m)[23617]; who != "" {
		t.Errorf("a call with other durations named the request: %q", who)
	}
}

// A call that was cut short carries no timings. It names the request that the
// server released when the caller hung up, and no other.
func TestCallerOfAbortedRequest(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.Local)
	m := newModel(config{interval: 500 * time.Millisecond})
	m.Update(journalMsg{events: []journalEvent{
		{req: &Request{Task: 1, Start: t0, End: t0.Add(4 * time.Second), Done: true, PromptTokens: 50, GenTokens: 80}},
		{req: &Request{Task: 2, Start: t0.Add(60 * time.Second), End: t0.Add(63 * time.Second), Done: true, Cancelled: true}},
	}})
	m.Update(tapMsg{events: []tap.Event{
		{Ev: tap.Done, ID: 5, Who: "Rico", Method: "POST", Aborted: true, Start: t0.Add(59 * time.Second), End: t0.Add(61 * time.Second)},
		// Not a completion and not aborted: a POST without timings names nothing.
		{Ev: tap.Done, ID: 6, Who: "Motoko", Method: "POST", Path: "/tokenize", Start: t0, End: t0.Add(time.Second)},
		// llama-server was down: this call never reached a slot.
		{Ev: tap.Done, ID: 7, Who: "Motoko", Method: "POST", Status: 502, Error: "connection refused", Start: t0, End: t0},
	}})
	if got := whoOf(m); got[1] != "" || got[2] != "Rico" {
		t.Errorf("callers: %v", got)
	}
}

// While a request runs, its caller shows in the slots panel and in the first
// row of the request list.
func TestLiveCaller(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_busy.json")) // slot 0 busy, first seen now
	now := m.now
	if out := text(screen(m, 140, 40)); !strings.Contains(out, "task 23649") || strings.Contains(out, "Motoko") {
		t.Fatalf("without nltap the slot shows its task number:\n%s", out)
	}

	// Two calls are open: Motoko's arrived first, Rico's waits for the slot.
	m.Update(tapMsg{events: []tap.Event{
		{Ev: tap.Up, Start: now.Add(-time.Hour)},
		{Ev: tap.Start, ID: 1, Who: "Motoko", Method: "POST", Start: now.Add(-300 * time.Millisecond)},
		{Ev: tap.Start, ID: 2, Who: "Rico", Method: "POST", Start: now.Add(-100 * time.Millisecond)},
	}})
	if got := m.liveCallers(); got[0] != "Motoko" || len(got) != 1 {
		t.Errorf("live callers: %v", got)
	}
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		out := text(screen(m, size[0], size[1]))
		if !strings.Contains(out, "● generating thinking on") || !strings.Contains(out, "Motoko") || strings.Contains(out, "Rico") {
			t.Errorf("%dx%d: slots panel does not name Motoko alone:\n%s", size[0], size[1], out)
		}
		// The live row starts with the marker and has the name in the who column.
		found := false
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "▶") && strings.Contains(l, " Motoko ") {
				found = true
			}
		}
		if !found {
			t.Errorf("%dx%d: the running request's row lacks the caller:\n%s", size[0], size[1], out)
		}
	}

	// Motoko's call ends; nltap restarts: nobody is in flight any more.
	m.Update(tapMsg{events: []tap.Event{{Ev: tap.Done, ID: 1, Who: "Motoko", Method: "POST", Start: now, End: now}}})
	if got := m.liveCallers(); got[0] != "Rico" {
		t.Errorf("after Motoko's call ended: %v", got)
	}
	m.Update(tapMsg{events: []tap.Event{{Ev: tap.Up, Start: now}}})
	if got := m.liveCallers(); len(got) != 0 {
		t.Errorf("after nltap restarted: %v", got)
	}

	// A request that was running before the only open call arrived came in
	// directly, not through nltap.
	m.Update(tapMsg{events: []tap.Event{{Ev: tap.Start, ID: 1, Who: "Rico", Method: "POST", Start: now.Add(30 * time.Second)}}})
	if got := m.liveCallers(); len(got) != 0 {
		t.Errorf("a later call named an earlier request: %v", got)
	}
}

// With many slots the caller's name stands in the one-line form.
func TestLiveCallerManySlots(t *testing.T) {
	m := testModel(t, manySlots(t, 12)) // slots 0, 3, 6, 9 are busy
	m.Update(tapMsg{events: []tap.Event{{Ev: tap.Start, ID: 1, Who: "Motoko", Method: "POST", Start: m.now.Add(-time.Second)}}})
	out := text(screen(m, 140, 40))
	if !strings.Contains(out, "● Motoko") {
		t.Errorf("no caller in the compact slot list:\n%s", out)
	}
}

func TestFollowTap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan tap.Event, 64)
	go followTap(ctx, path, 20*time.Millisecond, func(m tapMsg) {
		for _, e := range m.events {
			got <- e
		}
	})
	next := func() tap.Event {
		t.Helper()
		select {
		case e := <-got:
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("no event from the log")
			return tap.Event{}
		}
	}
	appendLine := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}

	time.Sleep(60 * time.Millisecond) // the log does not exist yet: that is fine
	appendLine(`{"ev":"up","start":"2026-11-27T10:00:00+01:00"}` + "\n" + `{"ev":"start","id":1,"who":"Motoko","method":"POST"}` + "\n")
	if e := next(); e.Ev != tap.Up {
		t.Errorf("first event: %+v", e)
	}
	if e := next(); e.Ev != tap.Start || e.Who != "Motoko" || e.ID != 1 {
		t.Errorf("second event: %+v", e)
	}

	// A line written in two parts is read once, whole. Lines that are not events are skipped.
	appendLine(`{"ev":"done","id":1,"wh`)
	time.Sleep(60 * time.Millisecond)
	appendLine(`o":"Motoko","status":200}` + "\nnot json\n{}\n")
	if e := next(); e.Ev != tap.Done || e.Who != "Motoko" || e.Status != 200 {
		t.Errorf("split line: %+v", e)
	}

	// nltap moves a large log aside and starts a new one.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	appendLine(`{"ev":"start","id":2,"who":"Rico","method":"POST"}` + "\n")
	if e := next(); e.Who != "Rico" || e.ID != 2 {
		t.Errorf("after the log was moved aside: %+v", e)
	}
}

// At start only the end of a long log is read.
func TestFollowTapLongLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	line := `{"ev":"done","id":1,"who":"Motoko","method":"GET","path":"/v1/models","status":200}` + "\n"
	n := tapBacklog/len(line) + 1000
	if err := os.WriteFile(path, []byte(strings.Repeat(line, n)+`{"ev":"start","id":2,"who":"Rico","method":"POST"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgs := make(chan tapMsg, 1)
	go followTap(ctx, path, 20*time.Millisecond, func(m tapMsg) { msgs <- m })
	select {
	case m := <-msgs:
		if got := len(m.events); got >= n || got < n-1100 {
			t.Errorf("%d events read of %d lines", got, n+1)
		}
		if last := m.events[len(m.events)-1]; last.Who != "Rico" {
			t.Errorf("last event: %+v", last)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read")
	}
}

// A user who may not read the journal is told so, with the reason and the way out.
func TestJournalDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may read everything")
	}
	dir := t.TempDir()
	machine := filepath.Join(dir, "b5d8961cf04f4c7bab5d6799721ce492")
	os.MkdirAll(machine, 0o755)
	file := filepath.Join(machine, "system.journal")
	os.WriteFile(file, []byte("journal"), 0o640)

	if got := journalDenied([]string{dir, filepath.Join(dir, "missing")}, "wjk"); got != "" {
		t.Errorf("readable journal reported as denied: %q", got)
	}
	os.Chmod(file, 0)
	got := journalDenied([]string{dir}, "wjk")
	for _, want := range []string{"The journal can't be read: wjk has no permission", dir, "sudo usermod -aG systemd-journal wjk", "log in again", "as root"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q: %q", want, got)
		}
	}
	// No journal on this machine at all: nothing to say about permissions.
	if got := journalDenied([]string{filepath.Join(dir, "missing")}, "wjk"); got != "" {
		t.Errorf("missing journal reported as denied: %q", got)
	}

	// The screen says it in both places, instead of "no request since yesterday".
	m := newModel(config{unit: "llama-server", interval: 500 * time.Millisecond, version: "test"})
	m.Update(pollMsg{at: time.Now(), slots: readSlots(t, "slots_idle.json"), hasHealth: true, health: healthOK})
	m.Update(journalMsg{note: got})
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		out := text(screen(m, size[0], size[1]))
		flat := strings.Join(strings.Fields(strings.NewReplacer("│", " ", "╮", " ", "╭", " ").Replace(out)), " ")
		if !strings.Contains(out, "last   The journal can't be read") {
			t.Errorf("%dx%d: slots panel does not say the journal can't be read:\n%s", size[0], size[1], out)
		}
		if size[0] == 140 && !strings.Contains(flat, "sudo usermod -aG systemd-journal wjk") {
			t.Errorf("%dx%d: requests panel lacks the way out:\n%s", size[0], size[1], out)
		}
		if strings.Contains(out, "no request in the journal") {
			t.Errorf("%dx%d: still says there was no request:\n%s", size[0], size[1], out)
		}
	}
}
