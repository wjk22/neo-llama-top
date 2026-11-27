package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func width(s string) int { return lipgloss.Width(s) }

// fit makes s exactly w cells wide: cut with an ellipsis or padded on the right.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if n := width(s); n > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(0, w-width(s)))
}

// fitRight is fit with the padding on the left.
func fitRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if n := width(s); n > w {
		s = ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", max(0, w-width(s))) + s
}

// spread puts left and right at the two ends of a line w cells wide. When
// both do not fit, the left part is cut.
func spread(left, right string, w int) string {
	rw := width(right)
	if rw >= w {
		return fit(right, w)
	}
	return fit(left, w-rw) + right
}

// center places s in the middle of a line w cells wide.
func center(s string, w int) string {
	n := width(s)
	if n >= w {
		return fit(s, w)
	}
	l := (w - n) / 2
	return strings.Repeat(" ", l) + s + strings.Repeat(" ", w-n-l)
}

// edge describes one border line of a box: titles on the left, one in the
// middle, more on the right. Each title is an already styled string.
type edge struct {
	left, right []string
	mid         string
}

// box draws a rounded frame w by h around lines, which are cut or padded to
// fit. Titles sit in the border between ┐ ┌ (top) or ┘ └ (bottom), as btop
// draws them.
func box(w, h int, st lipgloss.Style, top, bottom edge, lines []string) string {
	var b strings.Builder
	b.WriteString(st.Render("╭") + borderLine(w-2, st, "┐", "┌", top) + st.Render("╮"))
	for i := range h - 2 {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString("\n" + st.Render("│") + fit(line, w-2) + st.Render("│"))
	}
	b.WriteString("\n" + st.Render("╰") + borderLine(w-2, st, "┘", "└", bottom) + st.Render("╯"))
	return b.String()
}

func borderLine(w int, st lipgloss.Style, open, shut string, e edge) string {
	dash := func(n int) string { return st.Render(strings.Repeat("─", max(0, n))) }
	title := func(s string) string { return st.Render(open) + s + st.Render(shut) }

	var l, r string
	for _, s := range e.left {
		if t := dash(1) + title(s); width(l)+width(t) <= w {
			l += t
		}
	}
	// Right titles are dropped from the left when the line is too narrow.
	for i := len(e.right) - 1; i >= 0; i-- {
		if t := title(e.right[i]) + dash(1); width(l)+width(r)+width(t) <= w {
			r = t + r
		}
	}
	gap := w - width(l) - width(r)
	if e.mid == "" {
		return l + dash(gap) + r
	}
	m := title(e.mid)
	mw := width(m)
	if gap < mw+2 {
		return l + dash(gap) + r
	}
	// Centre on the whole line, but stay inside the gap.
	pos := min(max((w-mw)/2-width(l), 1), gap-mw-1)
	return l + dash(pos) + m + dash(gap-pos-mw) + r
}

// meter is a row of ■ cells, the first frac of them lit along the gradient.
func meter(w int, frac float64, g gradient) string {
	if w <= 0 {
		return ""
	}
	frac = min(max(frac, 0), 1)
	lit := int(frac*float64(w) + 0.5)
	if frac > 0 && lit == 0 {
		lit = 1
	}
	var b strings.Builder
	for i := range lit {
		b.WriteString(g.at(float64(i) / float64(max(1, w-1))).Render("■"))
	}
	b.WriteString(stFaint.Render(strings.Repeat("■", w-lit)))
	return b.String()
}

// Braille cells hold two columns of four dots. These are the dot patterns for
// one column filled with 0..4 dots, counted from the bottom or from the top.
var (
	dotsUpL   = [5]rune{0, 0x40, 0x44, 0x46, 0x47}
	dotsUpR   = [5]rune{0, 0x80, 0xA0, 0xB0, 0xB8}
	dotsDownL = [5]rune{0, 0x01, 0x03, 0x07, 0x47}
	dotsDownR = [5]rune{0, 0x08, 0x18, 0x38, 0xB8}
)

// brailleGraph draws vals (0..1, two per cell, oldest first) as a filled graph
// w cells wide and h cells high. With down set the graph hangs from the top.
// pick names the gradient of a column, and a number that identifies it; each
// column gets brighter towards its tip.
func brailleGraph(vals []float64, w, h int, down bool, pick func(col int) (gradient, int)) []string {
	level := func(i int) int {
		if i >= len(vals) || vals[i] <= 0 {
			return 0
		}
		return max(1, int(min(vals[i], 1)*float64(4*h)+0.5))
	}
	lines := make([]string, h)
	for row := range h {
		r := h - 1 - row // distance from the baseline, in cells
		if down {
			r = row
		}
		var b strings.Builder
		var run strings.Builder
		runKey := -1
		var runStyle lipgloss.Style
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(runStyle.Render(run.String()))
				run.Reset()
			}
		}
		for x := range w {
			nl, nr := level(2*x), level(2*x+1)
			kl, kr := min(max(nl-4*r, 0), 4), min(max(nr-4*r, 0), 4)
			var ch rune
			key := 0
			st := stFaint
			switch {
			case kl+kr > 0:
				bits := dotsUpL[kl] | dotsUpR[kr]
				if down {
					bits = dotsDownL[kl] | dotsDownR[kr]
				}
				ch = 0x2800 + bits
				col := 2 * x // the taller of the two columns sets the colour
				if nr >= nl {
					col++
				}
				g, id := pick(col)
				gi := g.index((float64(r) + 0.5) / float64(h))
				key, st = 1+id*1000+gi, g[gi]
			case r == 0 && down:
				ch = '⠉'
			case r == 0:
				ch = '⣀'
			default:
				ch = ' '
			}
			if key != runKey {
				flush()
				runKey, runStyle = key, st
			}
			run.WriteRune(ch)
		}
		flush()
		lines[row] = b.String()
	}
	return lines
}

// niceCeil rounds v up to 1, 1.5, 2, 3, 5 or 7.5 times a power of ten, for a
// graph scale that does not jump with every sample.
func niceCeil(v, floor float64) float64 {
	v = max(v, floor)
	p := 1.0
	for p*10 <= v {
		p *= 10
	}
	for p > v {
		p /= 10
	}
	for _, m := range []float64{1, 1.5, 2, 3, 5, 7.5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

// Number formats.

func commas(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// tokens shortens a token count: 842, 9.87k, 57.5k, 131k, 1.23M.
func tokens(n int) string {
	f := float64(n)
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return fmt.Sprintf("%.2fk", f/1e3)
	case n < 100_000:
		return fmt.Sprintf("%.1fk", f/1e3)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", f/1e3)
	}
	return fmt.Sprintf("%.2fM", f/1e6)
}

// rate formats tokens per second.
func rate(f float64) string {
	switch {
	case f <= 0:
		return "0"
	case f < 100:
		return fmt.Sprintf("%.1f", f)
	}
	return commas(int(f + 0.5))
}

// dur formats a duration at the precision that matters for its size.
func dur(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %02dh", int(d.Hours())/24, int(d.Hours())%24)
}

func gib(bytes float64) string { return fmt.Sprintf("%.1f GiB", bytes/(1<<30)) }

// paramCount formats a parameter count: 34.7B, 770M.
func paramCount(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return fmt.Sprint(n)
}
