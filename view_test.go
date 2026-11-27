package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The captured data is from a server on Central European Time; the screens
// are checked in that zone wherever the tests run.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("CET", 3600)
	os.Exit(m.Run())
}

// testModel is a model fed with the captured server answers: props, models,
// the journal, a GPU reading, and the given slots.
func testModel(t *testing.T, slots []Slot) *model {
	t.Helper()
	m := newModel(config{url: "http://127.0.0.1:8080", unit: "llama-server", interval: 500 * time.Millisecond, version: "0.1.1 (abc1234)"})
	var p Props
	var models struct {
		Data []struct {
			ID   string    `json:"id"`
			Meta ModelMeta `json:"meta"`
		} `json:"data"`
	}
	for name, v := range map[string]any{"props.json": &p, "models.json": &models} {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	meta := models.Data[0].Meta
	meta.ID = models.Data[0].ID

	// The journal fixture ends at 08:51:41 local time of the server (UTC+1).
	at := time.UnixMicro(1795765901815591).Add(5 * time.Second)
	m.Update(pollMsg{at: at, slots: slots, latency: 900 * time.Microsecond,
		hasHealth: true, health: healthOK, hasProps: true, props: &p, meta: &meta})

	f, err := os.Open("testdata/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	jp := newJournalParser()
	var evs []journalEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e journalEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if ev, ok := jp.parse(e.time(), e.PID, e.text()); ok {
			evs = append(evs, ev)
		}
	}
	m.Update(journalMsg{events: evs})
	g, _ := parseGPULine("0, NVIDIA GeForce RTX 3080 Ti Laptop GPU, 550.163.01, 56, 14683, 16384, 54, 87.31, [N/A], [N/A]")
	m.Update(gpuMsg{gpu: g})
	return m
}

func screen(m *model, w, h int) []string {
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return strings.Split(m.render(), "\n")
}

// Every screen must fill the terminal exactly: any panel set, any size.
func TestRenderFillsTheTerminal(t *testing.T) {
	states := map[string]*model{
		"idle":       testModel(t, readSlots(t, "slots_idle.json")),
		"busy":       testModel(t, readSlots(t, "slots_busy.json")),
		"no server":  newModel(config{url: "http://127.0.0.1:8080", interval: 500 * time.Millisecond, version: "0.1.1 (abc1234)"}),
		"many slots": testModel(t, manySlots(t, 12)),
	}
	down := testModel(t, nil)
	down.Update(pollMsg{at: down.now, slotsErr: &httpError{status: 501, msg: "This server does not support slots endpoint."},
		hasHealth: true, health: healthDown, healthMsg: "connection refused"})
	down.Update(journalMsg{note: "journalctl not found: request history needs systemd's journal"})
	down.Update(gpuMsg{note: "nvidia-smi not found: no NVIDIA driver on this machine?"})
	states["everything missing"] = down

	for name, m := range states {
		for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}, {237, 63}} {
			for mask := range 1 << panelCount {
				for i := range m.show {
					m.show[i] = mask&(1<<i) != 0
				}
				w, h := size[0], size[1]
				lines := screen(m, w, h)
				if len(lines) != h {
					t.Errorf("%s, %dx%d, panels %05b: %d lines", name, w, h, mask, len(lines))
					continue
				}
				for i, l := range lines {
					if width(l) != w {
						t.Errorf("%s, %dx%d, panels %05b: line %d is %d cells wide: %q", name, w, h, mask, i, width(l), ansi.Strip(l))
						break
					}
				}
			}
		}
	}
}

func manySlots(t *testing.T, n int) []Slot {
	t.Helper()
	var out []Slot
	for i := range n {
		s := readSlots(t, "slots_idle.json")[0]
		if i%3 == 0 {
			s = readSlots(t, "slots_busy.json")[0]
		}
		s.ID = i
		out = append(out, s)
	}
	return out
}

func text(lines []string) string { return ansi.Strip(strings.Join(lines, "\n")) }

func TestIdleScreen(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_idle.json"))
	out := text(screen(m, 140, 40))
	for _, want := range []string{
		"¹server", "²slots", "³tokens", "⁴requests", "⁵gpu",
		"● ok", "qwen3.6-35b-a3b", "Q4_K - Medium", "params 34.7B", "ctx 131,072", "trained 262,144", "build b1-1715275", "/metrics off",
		"○ idle 5.0s", "57.5k/131k", "44%",
		// the last request and today's totals, shown while idle
		"last   08:51:14 · took 27s", "prompt 32,518 @ 1,274 t/s", "gen    105 @ 49.0 t/s",
		"today  4 requests", "prompt 32.7k", "gen    907 @ 56.9 t/s",
		// the request list, newest first, with the column kept for the caller
		"time     who", "08:51:14 –", "02:03:57 –", "0 of 1 busy",
		"RTX 3080 Ti Laptop GPU", "driver 550.163.01", "14.3/16.0G", "54°C", "power 87 W", "fan –",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("idle screen lacks %q", want)
		}
	}
	if strings.Contains(out, "thinking") || strings.Contains(out, "answering") {
		t.Error("idle screen names a generation phase")
	}
}

func TestBusyScreen(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_busy.json"))
	out := text(screen(m, 140, 40))
	for _, want := range []string{
		"1 of 1 busy", "● generating thinking on", "task 23649",
		"prompt 32,518 new · 0 cached", "gen    48 of 200", "temp 1 · top_k 20 · top_p 0.95 · min_p 0.05",
		"today  4 requests",
		"▶", // the request in flight leads the list
	} {
		if !strings.Contains(out, want) {
			t.Errorf("busy screen lacks %q", want)
		}
	}
	if strings.Contains(out, "last   ") {
		t.Error("busy screen still shows the last request in the slots panel")
	}
	// Thinking is on and /slots does not split it: neither colour name may be claimed.
	if strings.Contains(out, "● thinking") || strings.Contains(out, "answering") {
		t.Error("busy screen claims a phase /slots cannot know")
	}

	// With thinking switched off, everything generated is the answer.
	s := readSlots(t, "slots_busy.json")
	s[0].Params.GenerationPrompt = "<|im_start|>assistant\n<think>\n\n</think>\n\n"
	out = text(screen(testModel(t, s), 140, 40))
	if !strings.Contains(out, "● answering") {
		t.Error("thinking off: slot not shown as answering")
	}
}

func TestPanelKeys(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_idle.json"))
	titles := []string{"¹server", "²slots", "³tokens", "⁴requests", "⁵gpu"}
	for i, title := range titles {
		key := tea.KeyPressMsg{Code: rune('1' + i), Text: fmt.Sprint(i + 1)}
		m.Update(key)
		if out := text(screen(m, 120, 36)); strings.Contains(out, title) {
			t.Errorf("key %d did not hide %s", i+1, title)
		}
		m.Update(key)
		if out := text(screen(m, 120, 36)); !strings.Contains(out, title) {
			t.Errorf("key %d did not bring %s back", i+1, title)
		}
	}
	for i := range titles {
		m.Update(tea.KeyPressMsg{Code: rune('1' + i), Text: fmt.Sprint(i + 1)})
	}
	if out := text(screen(m, 120, 36)); !strings.Contains(out, "all panels hidden") {
		t.Errorf("no hint with every panel hidden: %q", out)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Error("q does not quit")
	}
}

func TestSmallTerminal(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_idle.json"))
	if out := text(screen(m, 79, 24)); !strings.Contains(out, "nltop needs 80×24") {
		t.Errorf("no size notice: %q", out)
	}
}

// The request list scrolls back through the history and stops at its end.
func TestRequestScroll(t *testing.T) {
	m := testModel(t, readSlots(t, "slots_idle.json"))
	base := time.UnixMicro(1795765901815591)
	var evs []journalEvent
	for i := range 60 {
		st := base.Add(time.Duration(i+1) * time.Minute)
		evs = append(evs, journalEvent{req: &Request{Task: 30000 + i, Start: st, End: st.Add(2 * time.Second), Done: true, GenTokens: i}})
	}
	m.Update(journalMsg{events: evs})
	m.Update(pollMsg{at: base.Add(61 * time.Minute), slots: readSlots(t, "slots_idle.json")})

	out := text(screen(m, 120, 30))
	if !strings.Contains(out, "↑↓ scroll") || !strings.Contains(out, "09:51:41") {
		t.Fatalf("newest request not on top:\n%s", out)
	}
	for range 200 {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		screen(m, 120, 30)
	}
	out = text(screen(m, 120, 30))
	if !strings.Contains(out, "02:03:57") || !strings.Contains(out, "newer above") {
		t.Errorf("oldest request not reached by scrolling:\n%s", out)
	}
	if m.scroll != m.scrollMax {
		t.Errorf("scroll = %d, max %d", m.scroll, m.scrollMax)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if out = text(screen(m, 120, 30)); !strings.Contains(out, "09:51:41") {
		t.Error("Home does not return to the newest request")
	}
}
