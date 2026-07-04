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

// Palette is the full set of semantic colors hlab draws with. The built-in
// palettes use truecolor hex for a fixed look (mono uses ANSI 256 codes so it
// respects the terminal's own scheme); lipgloss degrades truecolor
// automatically on 256/16-color terminals.
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

	Heading  lipgloss.Color // brightest text (running guest names, panel headings)
	Faint    lipgloss.Color // most muted text (ids, table headers, footer descriptions)
	Line     lipgloss.Color // meter tracks / stronger rules
	LineSoft lipgloss.Color // header underrule, soft panel borders
	SelBG    lipgloss.Color // selected-row background (a concrete pre-blended color)
}

// palettes holds the built-in themes, keyed by lowercase name. "github-dark" is
// hlab's default look: what an unset/unknown theme resolves to (see Get) and
// what every omitted color field in a file theme ultimately falls back to — it
// MUST always exist.
var palettes = map[string]Palette{
	// github-dark: hlab's default. Matches the hlab.sh landing page design
	// tokens (truecolor).
	"github-dark": {
		Accent:  lipgloss.Color("#58a6ff"),
		Text:    lipgloss.Color("#d0d7de"),
		Dim:     lipgloss.Color("#8b949e"),
		Good:    lipgloss.Color("#3fb950"),
		Warn:    lipgloss.Color("#d29922"),
		Bad:     lipgloss.Color("#f85149"),
		Track:   lipgloss.Color("#30363d"),
		ModalBG: lipgloss.Color("#161b22"),
		OutBG:   lipgloss.Color("#0d1117"),
		OutFG:   lipgloss.Color("#d0d7de"),

		Heading:  lipgloss.Color("#f0f6fc"),
		Faint:    lipgloss.Color("#6e7681"),
		Line:     lipgloss.Color("#30363d"),
		LineSoft: lipgloss.Color("#21262d"),
		SelBG:    lipgloss.Color("#17273f"),
	},
	// one-dark: Atom's One Dark, the most popular IDE color scheme (replaces
	// the old ANSI "default" theme, which read too close to mono).
	"one-dark": {
		Accent:  lipgloss.Color("#61afef"),
		Text:    lipgloss.Color("#abb2bf"),
		Dim:     lipgloss.Color("#7f848e"),
		Good:    lipgloss.Color("#98c379"),
		Warn:    lipgloss.Color("#e5c07b"),
		Bad:     lipgloss.Color("#e06c75"),
		Track:   lipgloss.Color("#3e4451"),
		ModalBG: lipgloss.Color("#2c313a"),
		OutBG:   lipgloss.Color("#21252b"),
		OutFG:   lipgloss.Color("#abb2bf"),

		Heading:  lipgloss.Color("#dcdfe4"),
		Faint:    lipgloss.Color("#5c6370"),
		Line:     lipgloss.Color("#3e4451"),
		LineSoft: lipgloss.Color("#333842"),
		SelBG:    lipgloss.Color("#2e3947"), // accent @10% over #282c34
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

		Heading:  lipgloss.Color("#f8f8f2"),
		Faint:    lipgloss.Color("#6272a4"),
		Line:     lipgloss.Color("#44475a"),
		LineSoft: lipgloss.Color("#44475a"),
		SelBG:    lipgloss.Color("#33355c"),
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

		Heading:  lipgloss.Color("7"),
		Faint:    lipgloss.Color("8"),
		Line:     lipgloss.Color("240"),
		LineSoft: lipgloss.Color("240"),
		SelBG:    lipgloss.Color("#22262e"),
	},
}

// Get returns the palette for name (case-insensitive, surrounding space
// tolerated). An unknown or empty name falls back to github-dark, the default
// — theme selection never errors, so a typo in config.yaml just yields the
// standard look.
func Get(name string) Palette {
	if p, ok := palettes[strings.ToLower(strings.TrimSpace(name))]; ok {
		return p
	}
	return palettes["github-dark"]
}

// Names returns the built-in theme names: github-dark (the default) first,
// then the rest sorted.
func Names() []string {
	names := make([]string, 0, len(palettes))
	for n := range palettes {
		if n != "github-dark" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return append([]string{"github-dark"}, names...)
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
