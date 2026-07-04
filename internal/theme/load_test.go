package theme

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// withHomeDir points $HLAB_HOME at a fresh temp dir for the duration of the test,
// so theme loading/seeding never touches the operator's real ~/.hlab.
func withHomeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HLAB_HOME", dir)
	return dir
}

func TestLoadSeedsFile(t *testing.T) {
	dir := withHomeDir(t)
	p := filepath.Join(dir, "themes.yaml")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("themes.yaml should not exist before Load: %v", err)
	}
	set, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("Load should have seeded themes.yaml: %v", err)
	}
	// The seed ships the four built-ins, so the merged set exposes at least them.
	for _, n := range []string{"github-dark", "one-dark", "dracula", "mono"} {
		if !set.Has(n) {
			t.Errorf("seeded set missing built-in %q", n)
		}
	}
}

func TestLoadBuiltinsMatchWhenSeeded(t *testing.T) {
	withHomeDir(t)
	set, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The seeded file reproduces the compiled-in palettes exactly, so the merged
	// set's built-ins must equal Get() for each.
	for _, n := range []string{"github-dark", "one-dark", "dracula", "mono"} {
		if !reflect.DeepEqual(set.Get(n), Get(n)) {
			t.Errorf("seeded %q palette != built-in", n)
		}
	}
}

func TestLoadFileOverridesBuiltin(t *testing.T) {
	dir := withHomeDir(t)
	yaml := `themes:
  - name: dracula
    accent: "#000000"
`
	if err := os.WriteFile(filepath.Join(dir, "themes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := set.Get("dracula")
	if got.Accent != lipgloss.Color("#000000") {
		t.Errorf("file should override built-in accent, got %q", got.Accent)
	}
	// Unspecified fields fall back to the github-dark palette's value, not dracula's.
	def := palettes["github-dark"]
	if got.Text != def.Text {
		t.Errorf("missing field should fall back to github-dark text %q, got %q", def.Text, got.Text)
	}
}

func TestLoadAddsNewTheme(t *testing.T) {
	dir := withHomeDir(t)
	yaml := `themes:
  - name: solarized
    accent: "33"
    text: "230"
`
	if err := os.WriteFile(filepath.Join(dir, "themes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !set.Has("solarized") {
		t.Fatal("file theme should extend the set")
	}
	got := set.Get("solarized")
	if got.Accent != lipgloss.Color("33") {
		t.Errorf("ANSI accent should pass through, got %q", got.Accent)
	}
	if got.Text != lipgloss.Color("230") {
		t.Errorf("ANSI text should pass through, got %q", got.Text)
	}
	// Built-ins remain available alongside the added theme.
	if !set.Has("github-dark") {
		t.Error("built-ins should remain after adding a file theme")
	}
}

func TestSetGetUnknownFallsBack(t *testing.T) {
	set := builtinSet()
	if !reflect.DeepEqual(set.Get("nope"), set.Get("github-dark")) {
		t.Error("unknown name should fall back to github-dark")
	}
	if !reflect.DeepEqual(set.Get(""), set.Get("github-dark")) {
		t.Error("empty name should fall back to github-dark")
	}
}

func TestColorValuesHexAndANSI(t *testing.T) {
	ft := fileTheme{Accent: "#abcdef", Text: "15"}
	p := ft.palette()
	if p.Accent != lipgloss.Color("#abcdef") {
		t.Errorf("hex accent: got %q", p.Accent)
	}
	if p.Text != lipgloss.Color("15") {
		t.Errorf("ANSI text: got %q", p.Text)
	}
	// Empty fields fall back to the github-dark (default) palette.
	if p.Dim != palettes["github-dark"].Dim {
		t.Errorf("empty dim should fall back to github-dark, got %q", p.Dim)
	}
}

// TestNewFieldsChainFallback checks that a fileTheme setting only accent and
// dim resolves the five new roles against this SAME theme's already-resolved
// fields, not against the default palette's: Faint should track the custom
// Dim, while Line/LineSoft/SelBG (nothing upstream of them was customized)
// should equal the default-resolution chain (Track/Line/ModalBG).
func TestNewFieldsChainFallback(t *testing.T) {
	ft := fileTheme{Accent: "#111111", Dim: "#222222"}
	p := ft.palette()
	if p.Faint != lipgloss.Color("#222222") {
		t.Errorf("Faint should chain to the resolved Dim, got %q", p.Faint)
	}
	def := palettes["github-dark"]
	if p.SelBG != def.ModalBG {
		t.Errorf("SelBG should chain to the resolved ModalBG (github-dark's, since unset), got %q, want %q", p.SelBG, def.ModalBG)
	}
	if p.Line != def.Track {
		t.Errorf("Line should chain to the resolved Track (github-dark's, since unset), got %q, want %q", p.Line, def.Track)
	}
	if p.LineSoft != p.Line {
		t.Errorf("LineSoft should chain to the resolved Line, got %q, want %q", p.LineSoft, p.Line)
	}
}

// TestOldFormatThemeStillResolves ensures a pre-Phase-1, ten-key themes.yaml
// entry (no heading/faint/line/line_soft/sel_bg) still parses and yields a
// fully-populated palette — the whole point of the chained-fallback design.
func TestOldFormatThemeStillResolves(t *testing.T) {
	dir := withHomeDir(t)
	yaml := `themes:
  - name: oldschool
    accent: "#123456"
    text: "#abcdef"
    dim: "#333333"
    good: "#00ff00"
    warn: "#ffff00"
    bad: "#ff0000"
    track: "#444444"
    modal_bg: "#000000"
    out_bg: "#111111"
    out_fg: "#eeeeee"
`
	if err := os.WriteFile(filepath.Join(dir, "themes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := set.Get("oldschool")
	if got.Heading == "" || got.Faint == "" || got.Line == "" || got.LineSoft == "" || got.SelBG == "" {
		t.Fatalf("old-format theme should still yield non-empty new fields, got %+v", got)
	}
	if got.Heading != got.Text {
		t.Errorf("Heading should fall back to this theme's Text, got %q want %q", got.Heading, got.Text)
	}
	if got.Faint != got.Dim {
		t.Errorf("Faint should fall back to this theme's Dim, got %q want %q", got.Faint, got.Dim)
	}
	if got.Line != got.Track {
		t.Errorf("Line should fall back to this theme's Track, got %q want %q", got.Line, got.Track)
	}
	if got.LineSoft != got.Line {
		t.Errorf("LineSoft should fall back to this theme's Line, got %q want %q", got.LineSoft, got.Line)
	}
	if got.SelBG != got.ModalBG {
		t.Errorf("SelBG should fall back to this theme's ModalBG, got %q want %q", got.SelBG, got.ModalBG)
	}
}

func TestLoadBrokenFileFallsBackToBuiltins(t *testing.T) {
	dir := withHomeDir(t)
	if err := os.WriteFile(filepath.Join(dir, "themes.yaml"), []byte("themes: [ this is : not valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load()
	if err == nil {
		t.Error("Load should report a parse error for a broken file")
	}
	// ...but still return a usable built-in-only set so the binary never breaks.
	if set == nil || !set.Has("github-dark") {
		t.Fatal("Load must return a usable built-in set even on a broken file")
	}
}

func TestSetNamesSorted(t *testing.T) {
	set := builtinSet()
	set.set("aaa", palettes["github-dark"])
	names := set.Names()
	// github-dark (the default) is pinned first; the rest must be sorted.
	if names[0] != "github-dark" {
		t.Fatalf("expected github-dark pinned first, got %v", names)
	}
	rest := names[1:]
	for i := 1; i < len(rest); i++ {
		if rest[i-1] > rest[i] {
			t.Fatalf("Names() tail not sorted: %v", names)
		}
	}
	if rest[0] != "aaa" {
		t.Errorf("expected added theme first in the sorted tail, got %v", names)
	}
}
