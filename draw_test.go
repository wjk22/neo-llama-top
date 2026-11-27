package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestFit(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "}, {"abcdef", 4, "abc…"}, {"", 3, "   "}, {"abc", 0, ""},
	} {
		if got := fit(c.in, c.w); got != c.want {
			t.Errorf("fit(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
	if got := fitRight("42", 5); got != "   42" {
		t.Errorf("fitRight = %q", got)
	}
	if got := spread("left", "right", 12); got != "left   right" {
		t.Errorf("spread = %q", got)
	}
	if got := spread("a long left part", "right", 12); got != "a long…right" {
		t.Errorf("spread, too long = %q", got)
	}
	// Styled text is measured by what shows, not by its escape codes.
	if got := width(fit(stErr.Render("unreachable"), 6)); got != 6 {
		t.Errorf("styled text cut to %d cells, want 6", got)
	}
}

func TestBox(t *testing.T) {
	top := edge{left: []string{"¹server"}, mid: "12:00:00", right: []string{"1-5 panels", "q quit"}}
	for _, w := range []int{30, 44, 80, 200} {
		out := box(w, 5, stBoxServer, top, edge{left: []string{"today"}}, []string{"one", strings.Repeat("x", 300)})
		lines := strings.Split(out, "\n")
		if len(lines) != 5 {
			t.Fatalf("width %d: %d lines, want 5", w, len(lines))
		}
		for i, l := range lines {
			if width(l) != w {
				t.Errorf("width %d, line %d is %d cells: %q", w, i, width(l), ansi.Strip(l))
			}
		}
		plain := ansi.Strip(lines[0])
		if !strings.HasPrefix(plain, "╭─┐¹server┌") || !strings.HasSuffix(plain, "─╮") {
			t.Errorf("width %d, top border: %q", w, plain)
		}
		// Titles are dropped from the left as the box gets narrow; the title stays.
		if w >= 80 && (!strings.Contains(plain, "┐12:00:00┌") || !strings.Contains(plain, "┐1-5 panels┌─┐q quit┌─╮")) {
			t.Errorf("width %d, top border lost a title: %q", w, plain)
		}
		if !strings.HasPrefix(ansi.Strip(lines[4]), "╰─┘today└") {
			t.Errorf("width %d, bottom border: %q", w, ansi.Strip(lines[4]))
		}
	}
}

func TestMeter(t *testing.T) {
	for _, c := range []struct {
		frac float64
		lit  int
	}{{0, 0}, {0.001, 1}, {0.5, 10}, {1, 20}, {1.7, 20}, {-1, 0}} {
		m := meter(20, c.frac, gradCtx)
		if width(m) != 20 {
			t.Errorf("meter(%v) is %d cells wide", c.frac, width(m))
		}
		// Lit cells carry the gradient; the rest follow as one faint run.
		rest := ""
		if c.lit < 20 {
			rest = stFaint.Render(strings.Repeat("■", 20-c.lit))
		}
		if !strings.HasSuffix(m, rest) || strings.Count(strings.TrimSuffix(m, rest), "■") != c.lit {
			t.Errorf("meter(%v): want %d lit cells, got %q", c.frac, c.lit, ansi.Strip(m))
		}
	}
}

// The context gauge shades from teal towards amber as it fills.
func TestCtxGradientEndsInAmber(t *testing.T) {
	low, high := gradCtx.at(0).Render("x"), gradCtx.at(1).Render("x")
	if !strings.Contains(low, "63;143;122") { // #3f8f7a
		t.Errorf("empty end of the context gauge: %q", low)
	}
	if !strings.Contains(high, "255;176;32") { // #ffb020
		t.Errorf("full end of the context gauge: %q", high)
	}
}

// Thinking and answering must not share a colour, with each other or with
// generation whose split is unknown.
func TestPhaseColoursDiffer(t *testing.T) {
	seen := map[string]phase{}
	for _, p := range []phase{phPrompt, phGen, phThinking, phAnswering} {
		c := phaseStyle(p).Render("x")
		if other, dup := seen[c]; dup {
			t.Errorf("%s and %s share a colour", phaseName(p), phaseName(other))
		}
		seen[c] = p
	}
}

func TestBrailleGraph(t *testing.T) {
	pick := func(int) (gradient, int) { return gradUtil, 0 }
	vals := []float64{0, 0, 0.5, 0.5, 1, 1, 0.01, 0}
	up := brailleGraph(vals, 4, 2, false, pick)
	if len(up) != 2 {
		t.Fatalf("%d rows, want 2", len(up))
	}
	top, bottom := []rune(ansi.Strip(up[0])), []rune(ansi.Strip(up[1]))
	if string(top) != "  ⣿ " {
		t.Errorf("top row = %q", string(top))
	}
	// Empty cells show the baseline, half fills the bottom cell, a trace still shows one dot.
	if string(bottom) != "⣀⣿⣿⡀" {
		t.Errorf("bottom row = %q", string(bottom))
	}
	down := brailleGraph(vals, 4, 2, true, pick)
	if got := ansi.Strip(down[0]) + "|" + ansi.Strip(down[1]); got != "⠉⣿⣿⠁|  ⣿ " {
		t.Errorf("hanging graph = %q", got)
	}
	for _, l := range append(up, down...) {
		if width(l) != 4 {
			t.Errorf("row %q is %d cells wide", ansi.Strip(l), width(l))
		}
	}
}

func TestFormats(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1.00k", 8323: "8.32k", 57531: "57.5k", 131072: "131k", 1053211: "1.05M"} {
		if got := tokens(in); got != want {
			t.Errorf("tokens(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 131072: "131,072", 1234567: "1,234,567"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "0", 42.64: "42.6", 99.96: "100.0", 1273.93: "1,274"} {
		if got := rate(in); got != want {
			t.Errorf("rate(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		3400 * time.Millisecond: "3.4s", 27 * time.Second: "27s", 724 * time.Second: "12m 04s",
		7*time.Hour + 2*time.Minute: "7h 02m", 76 * time.Hour: "3d 04h",
	} {
		if got := dur(in); got != want {
			t.Errorf("dur(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]float64{0: 10, 9: 10, 42: 50, 61: 75, 76: 100, 1274: 1500, 1463: 1500, 1600: 2000} {
		if got := niceCeil(in, 10); got != want {
			t.Errorf("niceCeil(%v) = %v, want %v", in, got, want)
		}
	}
	if got := paramCount(34660610688); got != "34.7B" {
		t.Errorf("paramCount = %q", got)
	}
}
