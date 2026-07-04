package theme

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestGetEmptyIsGithubDark(t *testing.T) {
	if !reflect.DeepEqual(Get(""), Get("github-dark")) {
		t.Fatal(`Get("") should equal Get("github-dark")`)
	}
}

func TestGetCaseInsensitive(t *testing.T) {
	if !reflect.DeepEqual(Get("Dracula"), Get("dracula")) {
		t.Fatal(`Get("Dracula") should equal Get("dracula")`)
	}
	if !reflect.DeepEqual(Get("  MONO "), Get("mono")) {
		t.Fatal(`Get("  MONO ") should equal Get("mono")`)
	}
}

func TestGetUnknownFallsBack(t *testing.T) {
	if !reflect.DeepEqual(Get("nope-not-a-theme"), Get("github-dark")) {
		t.Fatal("unknown theme should fall back to github-dark")
	}
}

func TestNames(t *testing.T) {
	got := Names()
	// github-dark (the default) is pinned first; the rest are sorted.
	want := []string{"github-dark", "dracula", "mono", "one-dark"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestGetGithubDark(t *testing.T) {
	p := Get("github-dark")
	if p.Accent != "#58a6ff" {
		t.Fatalf("github-dark accent = %q, want #58a6ff", p.Accent)
	}
}

func TestNamesIncludesGithubDark(t *testing.T) {
	found := false
	for _, n := range Names() {
		if n == "github-dark" {
			found = true
		}
	}
	if !found {
		t.Fatal("Names() should include github-dark")
	}
}

// TestBuiltinsHaveAllFields verifies every built-in palette fills all 15
// semantic roles (the original 10 plus the 5 added for the TUI restyle), so no
// built-in silently renders with a zero-value (empty string) lipgloss.Color.
func TestBuiltinsHaveAllFields(t *testing.T) {
	for _, name := range Names() {
		p := Get(name)
		fields := map[string]lipgloss.Color{
			"Accent":   p.Accent,
			"Text":     p.Text,
			"Dim":      p.Dim,
			"Good":     p.Good,
			"Warn":     p.Warn,
			"Bad":      p.Bad,
			"Track":    p.Track,
			"ModalBG":  p.ModalBG,
			"OutBG":    p.OutBG,
			"OutFG":    p.OutFG,
			"Heading":  p.Heading,
			"Faint":    p.Faint,
			"Line":     p.Line,
			"LineSoft": p.LineSoft,
			"SelBG":    p.SelBG,
		}
		for field, v := range fields {
			if v == "" {
				t.Errorf("palette %q field %s is empty", name, field)
			}
		}
	}
}

// TestHuh checks the huh theme builder produces a usable theme for every built-in
// palette and threads the palette's accent through to the focused title (so the
// forms look like part of hlab). It is a pure transform — no terminal needed.
func TestHuh(t *testing.T) {
	for _, name := range Names() {
		p := Get(name)
		th := Huh(p)
		if th == nil {
			t.Fatalf("Huh(%q) returned nil", name)
		}
		if got := th.Focused.Title.GetForeground(); got != p.Accent {
			t.Errorf("Huh(%q) focused title fg = %v, want the palette accent %v", name, got, p.Accent)
		}
		if got := th.Focused.ErrorMessage.GetForeground(); got != p.Bad {
			t.Errorf("Huh(%q) error message fg = %v, want the palette bad color %v", name, got, p.Bad)
		}
	}
}
