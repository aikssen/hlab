package cmd

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/aikssen/hlab/internal/theme"
)

// cmdHuhTheme returns the huh form theme for the active CLI palette. cmdPalette
// is set from the loaded config by loadStack (and by runSetup), so every CLI
// form matches the same theme as the result boxes and the dashboard.
func cmdHuhTheme() *huh.Theme {
	return theme.Huh(cmdPalette)
}

// confirmf shows a yes/no prompt and stores the answer in out.
func confirmf(out *bool, format string, a ...any) error {
	return huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(fmt.Sprintf(format, a...)).Value(out),
	)).WithTheme(cmdHuhTheme()).Run()
}
