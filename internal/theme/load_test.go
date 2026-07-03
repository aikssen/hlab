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
	// The seed ships the three built-ins, so the merged set exposes at least them.
	for _, n := range []string{"default", "dracula", "mono"} {
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
	for _, n := range []string{"default", "dracula", "mono"} {
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
	// Unspecified fields fall back to the DEFAULT palette's value, not dracula's.
	def := palettes["default"]
	if got.Text != def.Text {
		t.Errorf("missing field should fall back to default text %q, got %q", def.Text, got.Text)
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
	if !set.Has("default") {
		t.Error("built-ins should remain after adding a file theme")
	}
}

func TestSetGetUnknownFallsBack(t *testing.T) {
	set := builtinSet()
	if !reflect.DeepEqual(set.Get("nope"), set.Get("default")) {
		t.Error("unknown name should fall back to default")
	}
	if !reflect.DeepEqual(set.Get(""), set.Get("default")) {
		t.Error("empty name should fall back to default")
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
	// Empty fields fall back to the default palette.
	if p.Dim != palettes["default"].Dim {
		t.Errorf("empty dim should fall back to default, got %q", p.Dim)
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
	if set == nil || !set.Has("default") {
		t.Fatal("Load must return a usable built-in set even on a broken file")
	}
}

func TestSetNamesSorted(t *testing.T) {
	set := builtinSet()
	set.set("aaa", palettes["default"])
	names := set.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() not sorted: %v", names)
		}
	}
	if names[0] != "aaa" {
		t.Errorf("expected added theme first when sorted, got %v", names)
	}
}
