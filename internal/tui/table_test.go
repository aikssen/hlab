package tui

import (
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// eightCells builds a full 8-cell row from the given texts, all with style st.
func eightCells(st lipgloss.Style, texts ...string) []cell {
	cells := make([]cell, len(texts))
	for i, t := range texts {
		cells[i] = cell{text: t, style: st}
	}
	return cells
}

func sampleTexts() []string {
	return []string{"web", "vm", "6100", "pve1", "2/4GB", "192.168.1.10", "running", "docker"}
}

// TestRowLineWidthInvariant asserts every rendering variant keeps the row at
// exactly tableWidth(managedCols) display cells.
func TestRowLineWidthInvariant(t *testing.T) {
	initStyles(theme.Get("github-dark"))
	want := tableWidth(managedCols) // 89

	// 1. all-plain row (zero-value styles, no bg).
	plain := eightCells(lipgloss.NewStyle(), sampleTexts()...)
	// 2. per-cell-styled row (name heading, rest dim), no bg.
	styled := eightCells(dimStyle, sampleTexts()...)
	styled[0].style = headingStyle
	// 3. selected row (bg = selBG).
	selected := eightCells(headingStyle, sampleTexts()...)
	// 4. row containing a pre-styled meter cell built with meterBarBG onto selBG.
	withPre := eightCells(headingStyle, sampleTexts()...)
	withPre[4] = cell{pre: meterBarBG(0.5, 6, selBG)}

	cases := []struct {
		name  string
		cells []cell
		bg    lipgloss.Color
	}{
		{"all-plain", plain, ""},
		{"per-cell-styled", styled, ""},
		{"selected", selected, selBG},
		{"pre-cell-selected", withPre, selBG},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ansi.StringWidth(rowLine(tc.cells, managedCols, tc.bg))
			if got != want {
				t.Fatalf("width = %d, want %d", got, want)
			}
		})
	}
}

// TestRowLineSelectedComposition asserts a selected row keeps each cell's own
// foreground AND carries the bg into the trailing padding region.
func TestRowLineSelectedComposition(t *testing.T) {
	// Force a color profile: under `go test` stdout is not a TTY, so lipgloss
	// defaults to the Ascii profile and strips all SGR — this test asserts on
	// the emitted color escapes, so it needs real ANSI output.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	initStyles(theme.Get("github-dark"))

	cells := eightCells(dimStyle, sampleTexts()...)
	cells[0].style = headingStyle // bright name cell
	// Give the last (PROVISIONED, width 20) column short text so it has ample
	// trailing padding that must carry the bg.
	cells[7].text = "x"
	out := rowLine(cells, managedCols, selBG)

	// Per-cell fg survives: the heading fg color params appear in the output,
	// even though lipgloss folds fg+bg into a single combined SGR sequence.
	fgParams := sgrParams(headingStyle.Render("X"))
	if fgParams == "" || !strings.Contains(out, fgParams) {
		t.Fatalf("heading fg params %q not found in selected row output", fgParams)
	}

	// The row bg is baked into every span (lipgloss won't re-fill behind
	// already-styled spans), so the bg params must appear more than once.
	bgParams := sgrParams(lipgloss.NewStyle().Background(selBG).Render("X"))
	if bgParams == "" {
		t.Fatal("could not derive bg SGR params")
	}
	if n := strings.Count(out, bgParams); n < 2 {
		t.Fatalf("bg params appear %d times, want >= 2 (bg baked into every span)", n)
	}

	// A trailing padding region exists and carries the bg: the stripped row ends
	// in spaces, and the bg params occur inside the final rendered span.
	if !strings.HasSuffix(ansi.Strip(out), " ") {
		t.Fatal("expected trailing padding spaces in row")
	}
	lastReset := strings.LastIndex(out, "\x1b[0m")
	lastOpen := strings.LastIndex(out[:lastReset], "\x1b[")
	if !strings.Contains(out[lastOpen:lastReset], bgParams) {
		t.Fatal("final span does not carry the selected-row bg")
	}
}

// sgrParams returns the inner parameter bytes of the first SGR opening sequence
// in s, e.g. "38;2;88;166;255" — robust to fg/bg being combined into one code.
func sgrParams(s string) string {
	open := strings.Index(s, "\x1b[")
	if open < 0 {
		return ""
	}
	m := strings.IndexByte(s[open:], 'm')
	if m < 0 {
		return ""
	}
	return s[open+2 : open+m]
}

// TestRowLineTruncation asserts an over-long cell (text or pre) still yields a
// total width of exactly tableWidth(managedCols).
func TestRowLineTruncation(t *testing.T) {
	initStyles(theme.Get("github-dark"))
	want := tableWidth(managedCols)

	long := "this-is-a-very-long-guest-name-that-overflows"

	// text overflow in the NAME column (width 17).
	textCells := eightCells(headingStyle, sampleTexts()...)
	textCells[0].text = long
	if got := ansi.StringWidth(rowLine(textCells, managedCols, selBG)); got != want {
		t.Fatalf("text-overflow width = %d, want %d", got, want)
	}

	// pre overflow: a wide meter dropped into the CPU/RAM column (width 8).
	preCells := eightCells(headingStyle, sampleTexts()...)
	preCells[4] = cell{pre: meterBarBG(1.0, 20, selBG)} // 20 cells into an 8-wide col
	if got := ansi.StringWidth(rowLine(preCells, managedCols, selBG)); got != want {
		t.Fatalf("pre-overflow width = %d, want %d", got, want)
	}
}

// tableModel builds a bare dashboard model for section-rendering tests.
func tableModel(t *testing.T) Model {
	t.Helper()
	initStyles(theme.Get("github-dark"))
	store := state.New(t.TempDir())
	eng := engine.New(&config.Config{}, store, nil, nil)
	return New(eng, nil, "v0")
}

// dataRows returns the rendered section rows that carry the given needle (a guest
// name), one per matching line, ANSI stripped.
func rowFor(t *testing.T, section, needle string) (raw, plain string) {
	t.Helper()
	for line := range strings.SplitSeq(section, "\n") {
		if p := ansi.Strip(line); strings.Contains(p, needle) {
			return line, p
		}
	}
	t.Fatalf("no row containing %q in:\n%s", needle, section)
	return "", ""
}

// TestManagedRowDotAndGutter: a running guest row shows ●, a stopped one ○, and
// the selected row leads with the accent gutter "▎" while an unselected one leads
// with a blank.
func TestManagedRowDotAndGutter(t *testing.T) {
	m := tableModel(t)
	m.vms = []*state.VMSpec{
		{Name: "web", VMID: 6100, Type: "vm", Node: "pve1", MemoryGB: 4},
		{Name: "db", VMID: 6101, Type: "vm", Node: "pve1", MemoryGB: 2},
	}
	m.statuses = map[string]string{"web": "running", "db": "stopped"}
	m.cursor = 0
	out := m.managedSection(10)

	_, web := rowFor(t, out, "web")
	if !strings.Contains(web, "●") {
		t.Errorf("running row should carry the ● dot, got %q", web)
	}
	if !strings.HasPrefix(web, "▎") {
		t.Errorf("selected row should lead with the gutter ▎, got %q", web)
	}
	if w := ansi.StringWidth(web); w != tableWidth(managedCols) {
		t.Errorf("row width = %d, want %d", w, tableWidth(managedCols))
	}

	_, db := rowFor(t, out, "db")
	if !strings.Contains(db, "○") {
		t.Errorf("stopped row should carry the ○ dot, got %q", db)
	}
	if !strings.HasPrefix(db, " ") {
		t.Errorf("unselected row should lead with a blank gutter, got %q", db)
	}
}

// TestManagedRowDriftMarker: a drifted guest's name cell carries a "!" and the row
// keeps its full width.
func TestManagedRowDriftMarker(t *testing.T) {
	m := tableModel(t)
	m.vms = []*state.VMSpec{{Name: "web", VMID: 6100, Type: "vm", Node: "pve1", MemoryGB: 4}}
	m.statuses = map[string]string{"web": "running"}
	m.drift = map[string]engine.DriftStatus{
		"web": {Name: "web", State: "drifted", Attrs: []string{"cpu.cores"}},
	}
	out := m.managedSection(10)
	_, web := rowFor(t, out, "web")
	if !strings.Contains(web, "!") {
		t.Errorf("drifted row should carry a ! marker, got %q", web)
	}
	if w := ansi.StringWidth(web); w != tableWidth(managedCols) {
		t.Errorf("drifted row width = %d, want %d", w, tableWidth(managedCols))
	}
}

// TestHeaderLineLowercase: the header renders lowercase column titles and no
// longer carries the removed STATUS column.
func TestHeaderLineLowercase(t *testing.T) {
	initStyles(theme.Get("github-dark"))
	h := ansi.Strip(headerLine(managedCols))
	for _, want := range []string{"name", "kind", "id", "node", "cpu", "mem", "ip", "provisioned"} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing lowercase title %q, got %q", want, h)
		}
	}
	for _, gone := range []string{"NAME", "STATUS", "CPU/RAM", "PROVISIONED"} {
		if strings.Contains(h, gone) {
			t.Errorf("header should not contain old/uppercase title %q, got %q", gone, h)
		}
	}
}

// TestDiscoveredRowWidth: a discovered guest row uses the same layout and keeps the
// full table width (and the section title is lowercase).
func TestDiscoveredRowWidth(t *testing.T) {
	m := tableModel(t)
	m.guests = []proxmox.Guest{
		{Name: "other", VMID: 200, Type: "lxc", Node: "pve1", Status: "running", CPUFrac: 0.2, MemMB: 512},
	}
	out := m.discoveredSection(10)
	if !strings.Contains(ansi.Strip(out), "discovered") {
		t.Errorf("discovered section title should be lowercase, got:\n%s", out)
	}
	_, row := rowFor(t, out, "other")
	if !strings.Contains(row, "●") {
		t.Errorf("running discovered row should carry the ● dot, got %q", row)
	}
	if w := ansi.StringWidth(row); w != tableWidth(discoveredCols) {
		t.Errorf("discovered row width = %d, want %d", w, tableWidth(discoveredCols))
	}
}

// TestMemTag locks the single-letter memory formatting used in the mem column.
func TestMemTag(t *testing.T) {
	cases := []struct {
		mb   int
		want string
	}{
		{4096, "4G"}, {2048, "2G"}, {512, "512M"}, {0, "—"}, {1536, "1536M"},
	}
	for _, tc := range cases {
		if got := memTag(tc.mb); got != tc.want {
			t.Errorf("memTag(%d) = %q, want %q", tc.mb, got, tc.want)
		}
	}
}
