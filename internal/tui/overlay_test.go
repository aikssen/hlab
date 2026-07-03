package tui

import (
	"strings"
	"testing"
)

func TestTruncateShortStringUnchanged(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate(short) = %q, want unchanged", got)
	}
}

func TestTruncateExactWidthUnchanged(t *testing.T) {
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("truncate(exact width) = %q, want unchanged", got)
	}
}

func TestTruncateLongStringGetsEllipsis(t *testing.T) {
	got := truncate("hello world", 5)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate(long) = %q, want it to end with an ellipsis", got)
	}
	if len([]rune(got)) > 5 {
		t.Errorf("truncate(long, 5) = %q, want at most 5 cells wide", got)
	}
}

func TestStripANSIRemovesEscapeSequences(t *testing.T) {
	colored := "\x1b[31mred text\x1b[0m"
	got := stripANSI(colored)
	if got != "red text" {
		t.Errorf("stripANSI(%q) = %q, want %q", colored, got, "red text")
	}
}

func TestStripANSIPlainTextUnchanged(t *testing.T) {
	if got := stripANSI("plain text"); got != "plain text" {
		t.Errorf("stripANSI(plain) = %q, want unchanged", got)
	}
}

func TestPadToHeightAddsBlankLines(t *testing.T) {
	got := padToHeight("a\nb", 5)
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("padToHeight() produced %d lines, want 5", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" {
		t.Errorf("padToHeight() should preserve the original lines, got %v", lines)
	}
	for i := 2; i < 5; i++ {
		if lines[i] != "" {
			t.Errorf("padToHeight() line %d = %q, want empty padding", i, lines[i])
		}
	}
}

func TestPadToHeightAlreadyTallEnoughUnchanged(t *testing.T) {
	in := "a\nb\nc\nd\ne"
	got := padToHeight(in, 3)
	if got != in {
		t.Errorf("padToHeight() on an already-tall string = %q, want unchanged %q", got, in)
	}
}

func TestOverlayPlacesForegroundWithinBackground(t *testing.T) {
	bg := "aaaaa\naaaaa\naaaaa"
	fg := "XX"
	got := overlay(bg, fg, 1, 1)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("overlay() produced %d lines, want 3 (background height preserved)", len(lines))
	}
	// Row 0 and row 2 are untouched background.
	if stripANSI(lines[0]) != "aaaaa" {
		t.Errorf("overlay() row 0 = %q, want untouched background", stripANSI(lines[0]))
	}
	if stripANSI(lines[2]) != "aaaaa" {
		t.Errorf("overlay() row 2 = %q, want untouched background", stripANSI(lines[2]))
	}
	// Row 1 should contain the foreground content with background on both sides.
	plain := stripANSI(lines[1])
	if !strings.Contains(plain, "XX") {
		t.Errorf("overlay() row 1 = %q, want it to contain the foreground %q", plain, fg)
	}
	if !strings.HasPrefix(plain, "a") {
		t.Errorf("overlay() row 1 = %q, want the background visible before the foreground", plain)
	}
}

func TestOverlayForegroundTallerThanBackgroundIsClipped(t *testing.T) {
	bg := "aaa"
	fg := "X\nY\nZ" // taller than a 1-line background
	got := overlay(bg, fg, 0, 0)
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Errorf("overlay() should not grow the background height, got %d lines: %v", len(lines), lines)
	}
}
