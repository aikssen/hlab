package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var updateUpgrade bool

var vmUpdateCmd = &cobra.Command{
	Use:   "update <name|id>",
	Short: "Re-provision a VM idempotently (--upgrade also upgrades packages/runtimes)",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpdate,
}

var ctUpdateCmd = &cobra.Command{
	Use:   "update <name|id>",
	Short: "Re-provision a container idempotently (--upgrade also upgrades packages/runtimes)",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpdate,
}

func init() {
	vmUpdateCmd.Flags().BoolVar(&updateUpgrade, "upgrade", false,
		"also apt-upgrade, upgrade mise runtimes, re-pull dotfiles, and self-update CLI tools")
	ctUpdateCmd.Flags().BoolVar(&updateUpgrade, "upgrade", false,
		"also apt-upgrade, upgrade mise runtimes, re-pull dotfiles, and self-update CLI tools")

	vmCmd.AddCommand(vmUpdateCmd)
	ctCmd.AddCommand(ctUpdateCmd)
}

// runUpdate implements `hlab vm update` / `hlab ct update`: it re-runs Ansible
// against an already-provisioned guest using its persisted software/dotfiles
// selection (engine.Update) — no wizard, no --software/--dotfiles flags. Unlike
// runVMProvision it does NOT early-out when the selection is empty: a "just the
// OS" guest is still reconcilable/updatable (e.g. re-applying baseline hardening
// or, with --upgrade, apt/mise/CLI upgrades). Shared between both command
// groups, like runVMStart/Stop/Reboot.
func runUpdate(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	vm, err := store.Load(name)
	if err != nil {
		return err
	}

	eng := newEngine(cfg, store, runner)
	eng.AnsibleVerbose = verbosity

	title := "Re-provisioning (Ansible)…"
	if updateUpgrade {
		title = "Updating + upgrading (Ansible)…"
	}
	if err := runStep(
		title+" (can take several minutes; -v for details)",
		func() error { return eng.Update(vm, updateUpgrade) },
	); err != nil {
		return err
	}

	suffix := ""
	if updateUpgrade {
		suffix = " (with upgrades)"
	}
	fmt.Printf("\n✓ Updated %q%s.\n", name, suffix)
	return nil
}
