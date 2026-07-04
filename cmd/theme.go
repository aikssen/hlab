package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/theme"
)

var themeCmd = &cobra.Command{
	Use:   "theme [name]",
	Short: "Show or change the color theme",
	Long: `Show the available color themes, or switch to one.

With no argument, lists every theme (built-ins plus any you added to
~/.hlab/themes.yaml) and marks the active one. With a name, sets it as the theme
for the dashboard TUI and the CLI result boxes (persisted to ~/.hlab/config.yaml).

Themes are data: edit ~/.hlab/themes.yaml to tweak colors or add your own — no
rebuild needed. In the dashboard, the 't' key opens a live selector.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTheme,
}

func init() {
	rootCmd.AddCommand(themeCmd)
}

func runTheme(_ *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	set, _ := theme.Load() // never nil: falls back to the built-ins on any error

	// Resolve the active theme name to what actually renders (unknown/empty →
	// github-dark), so the list marker and the confirm box are honest.
	active := strings.ToLower(strings.TrimSpace(cfg.Theme))
	if !set.Has(active) {
		active = "github-dark"
	}

	if len(args) == 0 {
		cmdPalette = set.Get(active)
		accent := lipgloss.NewStyle().Foreground(cmdPalette.Accent).Bold(true)
		dim := lipgloss.NewStyle().Foreground(cmdPalette.Dim)
		for _, n := range set.Names() {
			if n == active {
				fmt.Println(accent.Render("* "+n) + dim.Render("  (active)"))
			} else {
				fmt.Println("  " + n)
			}
		}
		return nil
	}

	name := strings.ToLower(strings.TrimSpace(args[0]))
	if !set.Has(name) {
		return fmt.Errorf("unknown theme %q — available: %s", args[0], strings.Join(set.Names(), ", "))
	}
	cfg.Theme = name
	if err := cfg.Save(); err != nil {
		return err
	}

	// Confirm in the newly-selected palette so the change is visible at a glance.
	cmdPalette = set.Get(name)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cmdPalette.Good).
		Padding(0, 2).
		Render(lipgloss.NewStyle().Foreground(cmdPalette.Good).Render("✓ theme set to ") +
			lipgloss.NewStyle().Foreground(cmdPalette.Accent).Bold(true).Render(name))
	fmt.Println(box)
	return nil
}
