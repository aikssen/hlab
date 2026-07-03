package theme

import (
	"reflect"
	"testing"
)

func TestGetEmptyIsDefault(t *testing.T) {
	if !reflect.DeepEqual(Get(""), Get("default")) {
		t.Fatal(`Get("") should equal Get("default")`)
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
	if !reflect.DeepEqual(Get("nope-not-a-theme"), Get("default")) {
		t.Fatal("unknown theme should fall back to default")
	}
}

func TestNames(t *testing.T) {
	got := Names()
	want := []string{"default", "dracula", "mono"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
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
