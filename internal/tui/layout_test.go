package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
)

func testMetricsModel(t *testing.T) Model {
	t.Helper()
	store := state.New(t.TempDir())
	eng := engine.New(&config.Config{}, store, nil, nil)
	m := New(eng, nil, "v0")
	m.metrics = proxmox.ClusterMetricsData{
		Nodes: []proxmox.NodeMetric{
			{Name: "pve1", Status: "online", CPUFrac: 0.34, MemUsedMB: 11000, MemMaxMB: 16000},
			{Name: "pve2", Status: "online", CPUFrac: 0.12, MemUsedMB: 7000, MemMaxMB: 16000},
		},
		Storage: []proxmox.StorageMetric{
			{Name: "local-lvm", Node: "pve1", Status: "available", Content: "images", UsedGB: 62, TotalGB: 100},
			{Name: "local-lvm", Node: "pve2", Status: "available", Content: "images", UsedGB: 41, TotalGB: 100},
		},
	}
	return m
}

// TestMetricsPanelSize locks in the width/height the layout math depends on: the
// metrics panel must be exactly 40 wide (so detail 48 + gap 1 + metrics 40 == the
// 89-col table width) and 11 tall (9 content + border) to align with the detail
// panel under JoinHorizontal.
func TestMetricsPanelSize(t *testing.T) {
	m := testMetricsModel(t)
	v := m.metricsView()
	if w := lipgloss.Width(v); w != 40 {
		t.Errorf("metrics panel width = %d, want 40", w)
	}
	if h := lipgloss.Height(v); h != 11 {
		t.Errorf("metrics panel height = %d, want 11", h)
	}
}

// TestMetricsPanelContent checks the redesigned panel renders the fleet header,
// each node as a block with cpu/ram/disk meters, and braille fill.
func TestMetricsPanelContent(t *testing.T) {
	m := testMetricsModel(t)
	v := m.metricsView()
	for _, want := range []string{"cluster", "pve1", "pve2", "cpu", "ram", "dsk", "%", "⣿"} {
		if !strings.Contains(v, want) {
			t.Errorf("metrics panel should contain %q\n%s", want, v)
		}
	}
}

// TestNodeGuestCounts checks the per-node {running, total} tally: templates are
// excluded and only running guests count toward the first element.
func TestNodeGuestCounts(t *testing.T) {
	guests := []proxmox.Guest{
		{Name: "a", Node: "pve1", Status: "running"},
		{Name: "b", Node: "pve1", Status: "stopped"},
		{Name: "c", Node: "pve2", Status: "running"},
		{Name: "tmpl", Node: "pve1", Status: "stopped", Template: true},
	}
	got := nodeGuestCounts(guests)
	if got["pve1"] != [2]int{1, 2} {
		t.Errorf("pve1 count = %v, want [1 2]", got["pve1"])
	}
	if got["pve2"] != [2]int{1, 1} {
		t.Errorf("pve2 count = %v, want [1 1]", got["pve2"])
	}
	if _, ok := got["pve3"]; ok {
		t.Errorf("pve3 should have no entry, got %v", got["pve3"])
	}
}

// TestBottomRowMatchesTableWidth guards the invariant that keeps centering and the
// version alignment intact: detail + gap + metrics == the table width.
func TestBottomRowMatchesTableWidth(t *testing.T) {
	m := testMetricsModel(t)
	m.vms = []*state.VMSpec{{Name: "alpha", VMID: 6001, Type: "vm"}}
	row := lipgloss.JoinHorizontal(lipgloss.Top, m.detailView(), " ", m.metricsView())
	if w, want := lipgloss.Width(row), tableWidth(managedCols); w != want {
		t.Errorf("joined bottom row width = %d, want %d (table width)", w, want)
	}
}

// TestMetricsPanelLoadingState: with no metrics yet the panel still renders at the
// full fixed size and shows a loading hint (so JoinHorizontal alignment holds on
// the first frame before the async fetch returns).
func TestMetricsPanelLoadingState(t *testing.T) {
	store := state.New(t.TempDir())
	eng := engine.New(&config.Config{}, store, nil, nil)
	m := New(eng, nil, "v0")
	v := m.metricsView()
	if !strings.Contains(v, "loading") {
		t.Errorf("empty metrics should render a loading hint, got %q", v)
	}
	if w, h := lipgloss.Width(v), lipgloss.Height(v); w != 40 || h != 11 {
		t.Errorf("loading panel size = %dx%d, want 40x11", w, h)
	}
}

// TestVersionAlignsToTableNotFooter locks in the fix for the title-bar version
// escaping the table's right edge when a managed guest is selected. The footer
// keybinding line for a managed VM is wider than the tables, so aligning the
// version to max(body, footer) pushed it past the table; it must align to the
// table (body) width only.
func TestVersionAlignsToTableNotFooter(t *testing.T) {
	store := state.New(t.TempDir())
	eng := engine.New(&config.Config{}, store, nil, nil)
	m := New(eng, nil, "v9.9.9-deadbee")
	m.width, m.height = 200, 40
	m.vms = []*state.VMSpec{
		{Name: "alpha", VMID: 6001, Type: "vm"},
		{Name: "bravo", VMID: 6002, Type: "vm"},
	}
	m.cursor = 0 // a managed VM is selected -> the long footer is rendered

	view := m.dashboardView()
	titleLine, _, _ := strings.Cut(view, "\n")
	titleW := lipgloss.Width(titleLine)
	tableW := tableWidth(managedCols)

	if !strings.Contains(titleLine, "v9.9.9-deadbee") {
		t.Fatalf("title line should carry the version, got %q", titleLine)
	}
	if titleW > tableW {
		t.Errorf("title/version line width = %d, exceeds table width %d — the version escapes the table's right edge", titleW, tableW)
	}
}
