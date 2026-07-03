package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlay composites fg on top of bg at cell position (x, y), keeping the
// background visible around the foreground box (so a modal floats over the
// dashboard). Both are ANSI strings; the per-line cuts are SGR-aware so styles
// don't bleed across the seam.
func overlay(bg, fg string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")
	for i, fl := range fgLines {
		row := y + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		bl := bgLines[row]
		flw := ansi.StringWidth(fl)

		left := ansi.Truncate(bl, x, "")
		if lw := ansi.StringWidth(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ansi.TruncateLeft(bl, x+flw, "")
		bgLines[row] = left + "\x1b[0m" + fl + "\x1b[0m" + right
	}
	return strings.Join(bgLines, "\n")
}

// stripANSI removes all escape sequences from s, so streamed tool output renders
// as plain text the TUI can style uniformly (terraform/ansible emit their own
// colors/backgrounds, which otherwise look like selected text).
func stripANSI(s string) string {
	return ansi.Strip(s)
}

// truncate shortens an ANSI string to at most w cells, appending an ellipsis if
// it was cut (SGR-aware).
func truncate(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// padToHeight pads s with blank lines so it has at least h lines, so a centered
// overlay has full-height background to sit on.
func padToHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
