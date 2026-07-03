package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/aikssen/hlab/internal/state"
)

func TestDeclaredMemMB(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want int
	}{
		{"MemoryMB set wins", &state.VMSpec{MemoryMB: 512, MemoryGB: 4}, 512},
		{"falls back to MemoryGB*1024", &state.VMSpec{MemoryGB: 4}, 4096},
		{"zero everything", &state.VMSpec{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := declaredMemMB(tt.vm); got != tt.want {
				t.Errorf("declaredMemMB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMemShort(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"sub-GB container", &state.VMSpec{MemoryMB: 512}, "512MB"},
		{"whole GB via MemoryMB", &state.VMSpec{MemoryMB: 2048}, "2GB"},
		{"whole GB VM", &state.VMSpec{MemoryGB: 4}, "4GB"},
		{"odd MB VM (adopted)", &state.VMSpec{MemoryMB: 2560}, "2560MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memShort(tt.vm); got != tt.want {
				t.Errorf("memShort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCpuGaugeStoppedShowsDash(t *testing.T) {
	got := cpuGauge(0.5, false)
	if !strings.Contains(got, "—") {
		t.Errorf("cpuGauge(running=false) = %q, want a dash placeholder", got)
	}
}

func TestCpuGaugeRunningShowsPercentage(t *testing.T) {
	got := cpuGauge(0.42, true)
	if !strings.Contains(got, "42%") {
		t.Errorf("cpuGauge(0.42, true) = %q, want it to contain 42%%", got)
	}
}

func TestCpuGaugeRoundsToNearestPercent(t *testing.T) {
	got := cpuGauge(0.995, true)
	if !strings.Contains(got, "100%") {
		t.Errorf("cpuGauge(0.995, true) = %q, want it to round to 100%%", got)
	}
}

func TestRamGaugeStoppedShowsDash(t *testing.T) {
	got := ramGauge(1024, 4096, false)
	if !strings.Contains(got, "—") {
		t.Errorf("ramGauge(running=false) = %q, want a dash placeholder", got)
	}
}

func TestRamGaugeZeroMaxShowsDash(t *testing.T) {
	got := ramGauge(0, 0, true)
	if !strings.Contains(got, "—") {
		t.Errorf("ramGauge(maxMB=0) = %q, want a dash placeholder (avoid a div-by-zero)", got)
	}
}

func TestRamGaugeRunningShowsUsage(t *testing.T) {
	got := ramGauge(2048, 4096, true)
	if !strings.Contains(got, "2.0 / 4.0 GB") {
		t.Errorf("ramGauge(2048, 4096, true) = %q, want it to contain 2.0 / 4.0 GB", got)
	}
}

func TestHumanUptime(t *testing.T) {
	tests := []struct {
		sec  int64
		want string
	}{
		{30, "30s"},
		{90, "1m"},
		{3661, "1h1m"},
		{90000, "1d1h"}, // 25h = 1d1h
		{0, "0s"},
	}
	for _, tt := range tests {
		if got := humanUptime(tt.sec); got != tt.want {
			t.Errorf("humanUptime(%d) = %q, want %q", tt.sec, got, tt.want)
		}
	}
}

func TestHumanGB(t *testing.T) {
	tests := []struct {
		gb   int
		want string
	}{
		{0, "0G"}, {38, "38G"}, {512, "512G"}, {999, "999G"},
		{1000, "1.0T"}, {1843, "1.8T"}, {2048, "2.0T"}, {10240, "10.0T"},
	}
	for _, tt := range tests {
		if got := humanGB(tt.gb); got != tt.want {
			t.Errorf("humanGB(%d) = %q, want %q", tt.gb, got, tt.want)
		}
	}
}

func TestMeterBar(t *testing.T) {
	// Braille meter: filled cells are ⣿, the track is ⡀. Empty/full render the
	// boundary correctly and every fraction is exactly `cells` display columns.
	if got := meterBar(0, 10); strings.Contains(got, "⣿") {
		t.Errorf("meterBar(0,10) should have no filled cells, got %q", got)
	}
	if got := meterBar(1, 10); strings.Contains(got, "⡀") {
		t.Errorf("meterBar(1,10) should be entirely filled, got %q", got)
	}
	for _, f := range []float64{0, 0.1, 0.34, 0.5, 0.69, 0.92, 1} {
		if w := lipgloss.Width(meterBar(f, 12)); w != 12 {
			t.Errorf("meterBar(%v,12) width = %d, want 12 (braille glyphs must be width-1)", f, w)
		}
	}
	// Out-of-range fractions clamp instead of over/under-filling.
	if meterBar(-1, 10) != meterBar(0, 10) {
		t.Error("meterBar(-1,10) should clamp to meterBar(0,10)")
	}
	if meterBar(2, 10) != meterBar(1, 10) {
		t.Error("meterBar(2,10) should clamp to meterBar(1,10)")
	}
}
