// Package theme holds hlab's semantic color palette. Every color used by the
// dashboard TUI and the CLI result boxes is expressed as a role (Accent, Good,
// Bad, …) rather than a raw ANSI/hex literal, so the whole look can be swapped by
// selecting a different built-in palette via `theme:` in config.yaml.
//
// It deliberately lives outside internal/tui so that cmd (the CLI) can consume
// the same palette without importing the TUI package.
package theme

import (
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Palette is the full set of semantic colors hlab draws with. The default
// palette uses ANSI 256 codes (so it respects the terminal's own color scheme);
// the alternate palettes use truecolor hex where they want a fixed look.
type Palette struct {
	Accent  lipgloss.Color // titles, selectors, focus, borders on emphasis
	Text    lipgloss.Color // bright/primary text
	Dim     lipgloss.Color // descriptions, hints, muted borders
	Good    lipgloss.Color // success / selected / low-utilization gauge
	Warn    lipgloss.Color // warning / mid-utilization gauge
	Bad     lipgloss.Color // errors / high-utilization gauge
	Track   lipgloss.Color // rules and the unfilled part of a meter
	ModalBG lipgloss.Color // floating wizard-window background
	OutBG   lipgloss.Color // embedded "terminal" output-box background
	OutFG   lipgloss.Color // embedded "terminal" output-box foreground
}

// palettes holds the built-in themes, keyed by lowercase name. "default" MUST
// reproduce hlab's historical look exactly (same ANSI codes), so nothing changes
// for users who never set a theme.
var palettes = map[string]Palette{
	// default: the original ANSI-256 palette. Respects the terminal scheme.
	"default": {
		Accent:  lipgloss.Color("12"),
		Text:    lipgloss.Color("15"),
		Dim:     lipgloss.Color("8"),
		Good:    lipgloss.Color("10"),
		Warn:    lipgloss.Color("11"),
		Bad:     lipgloss.Color("9"),
		Track:   lipgloss.Color("240"),
		ModalBG: lipgloss.Color("#22262e"),
		OutBG:   lipgloss.Color("#0d1117"),
		OutFG:   lipgloss.Color("#d0d7de"),
	},
	// dracula: the well-known truecolor scheme.
	"dracula": {
		Accent:  lipgloss.Color("#bd93f9"),
		Text:    lipgloss.Color("#f8f8f2"),
		Dim:     lipgloss.Color("#6272a4"),
		Good:    lipgloss.Color("#50fa7b"),
		Warn:    lipgloss.Color("#f1fa8c"),
		Bad:     lipgloss.Color("#ff5555"),
		Track:   lipgloss.Color("#44475a"),
		ModalBG: lipgloss.Color("#282a36"),
		OutBG:   lipgloss.Color("#21222c"),
		OutFG:   lipgloss.Color("#f8f8f2"),
	},
	// mono: grayscale accent for accessibility, keeping the semantic good/warn/bad
	// colors so status still reads at a glance. Modal/output colors match default.
	"mono": {
		Accent:  lipgloss.Color("15"),
		Text:    lipgloss.Color("7"),
		Dim:     lipgloss.Color("8"),
		Good:    lipgloss.Color("10"),
		Warn:    lipgloss.Color("11"),
		Bad:     lipgloss.Color("9"),
		Track:   lipgloss.Color("240"),
		ModalBG: lipgloss.Color("#22262e"),
		OutBG:   lipgloss.Color("#0d1117"),
		OutFG:   lipgloss.Color("#d0d7de"),
	},
}

// Get returns the palette for name (case-insensitive, surrounding space
// tolerated). An unknown or empty name falls back to the default palette — theme
// selection never errors, so a typo in config.yaml just yields the default look.
func Get(name string) Palette {
	if p, ok := palettes[strings.ToLower(strings.TrimSpace(name))]; ok {
		return p
	}
	return palettes["default"]
}

// Names returns the sorted list of built-in theme names.
func Names() []string {
	names := make([]string, 0, len(palettes))
	for n := range palettes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Huh builds a huh form theme matching the given palette. It starts from the
// minimal base theme and recolors the focused/blurred field styles from the
// semantic palette so both the embedded TUI wizards and the CLI forms (`hlab
// setup`, the create wizard, confirms) look like part of hlab rather than a
// default huh form.
func Huh(p Palette) *huh.Theme {
	t := huh.ThemeBase()

	t.Focused.Base = t.Focused.Base.BorderForeground(p.Accent)
	t.Focused.Title = t.Focused.Title.Foreground(p.Accent).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(p.Accent).Bold(true).MarginBottom(1)
	t.Focused.Description = t.Focused.Description.Foreground(p.Dim)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(p.Bad)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(p.Bad)
	t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(p.Accent).SetString("› ")
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(p.Accent)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(p.Accent)
	t.Focused.Option = t.Focused.Option.Foreground(p.Text)
	t.Focused.MultiSelectSelector = lipgloss.NewStyle().Foreground(p.Accent).SetString("› ")
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(p.Good)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(p.Good).SetString("✓ ")
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(p.Dim).SetString("• ")
	t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(p.Text)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(p.Text).Background(p.Accent).Bold(true)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(p.Dim).Background(lipgloss.Color("0"))

	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(p.Good)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(p.Accent)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(p.Dim)

	// Blurred mirrors focused but hides the accent border.
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}
