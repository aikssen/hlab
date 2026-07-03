package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/proxmox"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that hlab's dependencies and configuration are healthy",
	RunE:  runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(_ *cobra.Command, _ []string) error {
	ok := true

	check := func(label string, err error) {
		if err != nil {
			ok = false
			fmt.Printf("✘ %s: %v\n", label, err)
		} else {
			fmt.Printf("✓ %s\n", label)
		}
	}

	check("terraform installed", binExists("terraform"))
	if err := binExists("ansible-playbook"); err != nil {
		fmt.Println("• ansible-playbook not found (only needed for provisioning, milestone M2)")
	} else {
		fmt.Println("✓ ansible-playbook installed")
	}
	if err := binExists("git"); err != nil {
		fmt.Println("• git not found (state versioning is skipped without it)")
	} else {
		fmt.Println("✓ git installed")
	}
	if err := binExists("ssh"); err != nil {
		fmt.Println("• ssh not found (needed for `hlab vm ssh` and provisioning)")
	} else {
		fmt.Println("✓ ssh installed")
	}

	if !config.Exists() {
		fmt.Println("✘ configuration: not set up — run `hlab setup`")
		return fmt.Errorf("configuration missing")
	}
	cfg, err := config.Load()
	check("configuration loaded", err)
	if err != nil {
		return err
	}

	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	check(fmt.Sprintf("Proxmox reachable (%s)", cfg.ProxmoxURL), pm.Ping())

	if nodes, err := pm.Nodes(); err == nil {
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			names = append(names, n.Name)
		}
		fmt.Printf("• nodes: %v\n", names)
	}
	fmt.Printf("• state dir: %s\n", cfg.StateDirExpanded())

	if !ok {
		return fmt.Errorf("some checks failed")
	}
	fmt.Println("\nAll good. Try: hlab vm create")
	return nil
}

func binExists(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("not found in PATH")
	}
	return nil
}
