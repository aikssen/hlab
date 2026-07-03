package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
)

// verbosity is the -v count: 0 = quiet (default), 1 = show tool output,
// 2 = also pass extra verbosity to the underlying tools.
var verbosity int

var rootCmd = &cobra.Command{
	Use:   "hlab",
	Short: "Create and provision Proxmox VMs the easy way",
	Long: `hlab is a platform tool for your homelab.

It discovers your Proxmox infrastructure, asks only what it cannot infer, and
orchestrates Terraform (and, later, Ansible) to create and provision VMs.

Start with:  hlab setup     then:  hlab vm create`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// Runs for every command (including bare `hlab`, whose config.Exists() gate in
	// tui.go must see the migrated file): consolidate a pre-M8 ~/.config/hlab into
	// ~/.hlab before anything reads it.
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		return config.MigrateLegacy()
	},
}

func init() {
	rootCmd.PersistentFlags().CountVarP(&verbosity, "verbose", "v",
		"show underlying tool output (-v); repeat for more detail (-vv)")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
