package main

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// The look follows btop's default theme: a dark background, muted box borders
// in a different hue per panel, red hotkey digits, and gradients on every
// meter and graph.

func fg(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

var (
	stText  = fg("#cccccc")
	stTitle = fg("#eeeeee").Bold(true)
	stHi    = fg("#b54040") // hotkey digits
	stLabel = fg("#8a8a8a")
	stDim   = fg("#5f5f5f")
	stFaint = fg("#353535") // graph baseline, unfilled meter cells
	stOK    = fg("#77ca9b")
	stWarn  = fg("#ffb020")
	stErr   = fg("#dc4c4c")

	// Box borders, one hue per panel.
	stBoxServer   = fg("#556d59")
	stBoxSlots    = fg("#6c6c4b")
	stBoxTokens   = fg("#5c588d")
	stBoxRequests = fg("#805252")
	stBoxGPU      = fg("#4b6f7a")
)

// gradient is a run of colours from low to high.
type gradient []lipgloss.Style

func newGradient(stops ...string) gradient {
	cs := make([]color.Color, len(stops))
	for i, s := range stops {
		cs[i] = lipgloss.Color(s)
	}
	g := gradient{}
	for _, c := range lipgloss.Blend1D(101, cs...) {
		g = append(g, lipgloss.NewStyle().Foreground(c))
	}
	return g
}

// index maps 0..1 to a position in the gradient.
func (g gradient) index(f float64) int {
	return int(min(max(f, 0), 1)*float64(len(g)-1) + 0.5)
}

func (g gradient) at(f float64) lipgloss.Style { return g[g.index(f)] }

var (
	// Thinking and answering are told apart by hue: violet and green.
	gradThinking  = newGradient("#4a2a80", "#8a52d0", "#e28bf2")
	gradAnswering = newGradient("#1e6b4a", "#4fbf82", "#b7f5a0")
	// Generation whose split is unknown stays uncoloured.
	gradGen    = newGradient("#4a5560", "#9aa7b5", "#e8eef5")
	gradPrompt = newGradient("#1d3f73", "#3f8fd8", "#7fe0ff")

	// The context gauge runs from teal to amber.
	gradCtx = newGradient("#3f8f7a", "#b9b45a", "#ffb020")

	gradUtil = newGradient("#77ca9b", "#cbc06c", "#dc4c4c")
	gradMem  = newGradient("#6a4a8a", "#b06ab3", "#ff6fa3")
	gradTemp = newGradient("#4897d4", "#5474e8", "#ff40b6")
)

func phaseGradient(p phase) gradient {
	switch p {
	case phPrompt:
		return gradPrompt
	case phThinking:
		return gradThinking
	case phAnswering:
		return gradAnswering
	}
	return gradGen
}

func phaseName(p phase) string {
	switch p {
	case phPrompt:
		return "prompt"
	case phGen:
		return "generating"
	case phThinking:
		return "thinking"
	case phAnswering:
		return "answering"
	}
	return "idle"
}

// phaseStyle is the bright end of a phase's gradient, for text.
func phaseStyle(p phase) lipgloss.Style {
	if p == phIdle {
		return stDim
	}
	return phaseGradient(p).at(0.8)
}
