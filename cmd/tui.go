package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/tui"
)

// init wires the bare `hlab` command (no subcommand) to launch the dashboard
// TUI (milestone M3). Subcommands (`hlab vm ...`, `setup`, `doctor`) are
// unaffected and remain the non-TUI / scripting path.
func init() {
	rootCmd.Args = cobra.NoArgs
	rootCmd.RunE = runTUI
}

func runTUI(_ *cobra.Command, _ []string) error {
	if !config.Exists() {
		fmt.Println("hlab isn't configured yet — run `hlab setup` first, then `hlab`.")
		return nil
	}
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	// Keep the runner quiet by default (captured) so background refreshes never
	// write to the alternate screen; the TUI streams long operations explicitly by
	// setting the runner's Out to its live log panel during each action. The TUI
	// needs the concrete Proxmox client for form/discovery calls (the engine wraps
	// it as the narrow engine.Proxmox interface), so build and pass it explicitly.
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	eng := engine.New(cfg, store, runner, pm)
	return tui.Run(eng, pm, fullVersion())
}
