package cmd

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/theme"
)

var (
	setupAddNode   string
	setupAddSSHKey bool

	// Non-interactive flags (useful for scripting / headless runs).
	setupURL          string
	setupTokenID      string
	setupTokenSecret  string
	setupNode         string
	setupStorage      string
	setupBridge       string
	setupInsecure     bool
	setupSSHKeys      []string
	setupGateway      string
	setupCIDR         int
	setupTemplate     string
	setupDotfilesRepo string
	setupCPUType      string
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure hlab (Proxmox connection and defaults)",
	Long: `Configure hlab's global settings, stored at ~/.hlab/config.yaml.

These are reused by every command, so the wizard never asks for them again.
Use --add-node or --add-ssh-key to extend an existing configuration.`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVar(&setupAddNode, "add-node", "", "add a Proxmox node to the configuration")
	setupCmd.Flags().BoolVar(&setupAddSSHKey, "add-ssh-key", false, "scan ~/.ssh and add a public key")

	setupCmd.Flags().StringVar(&setupURL, "url", "", "Proxmox URL (enables non-interactive setup)")
	setupCmd.Flags().StringVar(&setupTokenID, "token-id", "", "API token ID, e.g. hlab@pve!hlab")
	setupCmd.Flags().StringVar(&setupTokenSecret, "token-secret", "", "API token secret")
	setupCmd.Flags().StringVar(&setupNode, "node", "", "default node")
	setupCmd.Flags().StringVar(&setupStorage, "storage", "", "default storage (default local-lvm)")
	setupCmd.Flags().StringVar(&setupBridge, "bridge", "", "default network bridge (default vmbr0)")
	setupCmd.Flags().BoolVar(&setupInsecure, "insecure", false, "skip TLS verification")
	setupCmd.Flags().StringSliceVar(&setupSSHKeys, "ssh-key", nil, "SSH key name(s) from ~/.ssh (filename without .pub) to enable")
	setupCmd.Flags().StringVar(&setupGateway, "gateway", "", "default gateway for static IPs, e.g. 192.168.1.1")
	setupCmd.Flags().IntVar(&setupCIDR, "cidr", 0, "default subnet prefix, e.g. 24")
	setupCmd.Flags().StringVar(&setupTemplate, "template", "", "default template name to preselect in the wizard")
	setupCmd.Flags().StringVar(&setupCPUType, "cpu-type", "", "QEMU CPU model for new VMs (e.g. EPYC, host); empty keeps the portable default")
	setupCmd.Flags().StringVar(&setupDotfilesRepo, "dotfiles-repo", "", "dotfiles repo URL — ssh (git@host:path) or https for a public repo (enables the dotfiles software option)")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(_ *cobra.Command, _ []string) error {
	// Incremental modes operate on the existing config. --dotfiles-repo belongs
	// here too: it used to be honoured only inside runSetupNonInteractive, which is
	// reachable only via --url, so `hlab setup --dotfiles-repo <url>` silently fell
	// through to the interactive wizard — and just failed outright without a TTY.
	// Changing one field shouldn't mean re-supplying the whole connection.
	if setupAddNode != "" || setupAddSSHKey || ((setupDotfilesRepo != "" || setupCPUType != "") && setupURL == "") {
		return runSetupAdd()
	}
	// Non-interactive mode when --url is provided.
	if setupURL != "" {
		return runSetupNonInteractive()
	}

	cfg := &config.Config{Insecure: true}
	if config.Exists() {
		if c, err := config.Load(); err == nil {
			cfg = c
		}
	}
	// loadStack does not run for `hlab setup`, so seed the CLI palette from the
	// loaded config (default on first run, when no config exists yet).
	cmdPalette = theme.Get(cfg.Theme)

	// Step 1 — connection.
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Proxmox URL").Placeholder("https://proxmox.example:8006/").Value(&cfg.ProxmoxURL),
		huh.NewInput().Title("API Token ID").Placeholder("root@pam!hlab").Value(&cfg.TokenID),
		huh.NewInput().Title("API Token Secret").EchoMode(huh.EchoModePassword).Value(&cfg.TokenSecret),
		huh.NewConfirm().Title("Skip TLS verification? (self-signed certs)").Value(&cfg.Insecure),
	)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return err
	}

	// Verify and discover.
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	if err := pm.Ping(); err != nil {
		return fmt.Errorf("could not reach Proxmox with these credentials: %w", err)
	}
	nodes, err := pm.Nodes()
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	cfg.Nodes = cfg.Nodes[:0]
	nodeOpts := make([]huh.Option[string], 0, len(nodes))
	for _, n := range nodes {
		cfg.Nodes = append(cfg.Nodes, n.Name)
		nodeOpts = append(nodeOpts, huh.NewOption(n.Name, n.Name))
	}

	// Step 2 — discovery node. Storages and bridges are per-node resources, so hlab
	// queries this node to list them below. It does not decide where VMs run — that
	// follows the template chosen at create time.
	if cfg.DefaultNode == "" && len(nodes) > 0 {
		cfg.DefaultNode = nodes[0].Name
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Discovery node").
			Description("Node hlab queries to list storages and bridges (not where VMs run).").
			Options(nodeOpts...).Value(&cfg.DefaultNode),
	)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return err
	}

	// Step 3 — default storage & bridge (discovered on the discovery node); the
	// default template is listed across all nodes.
	storages, _ := pm.Storages(cfg.DefaultNode)
	bridges, _ := pm.Bridges(cfg.DefaultNode)
	templates, _ := pm.AllTemplates()
	storageOpts := optsFromStorages(storages, &cfg.DefaultStorage, "local-lvm")
	bridgeOpts := optsFromStrings(bridges, &cfg.DefaultBridge, "vmbr0")
	cidrStr := ""
	if cfg.DefaultCIDR > 0 {
		cidrStr = strconv.Itoa(cfg.DefaultCIDR)
	}

	fields := []huh.Field{
		huh.NewSelect[string]().Title("Default storage").Options(storageOpts...).Value(&cfg.DefaultStorage),
		huh.NewSelect[string]().Title("Default network bridge").Options(bridgeOpts...).Value(&cfg.DefaultBridge),
	}
	if len(templates) > 0 {
		var tmplOpts []huh.Option[string]
		seen := map[string]bool{}
		for _, t := range templates {
			if seen[t.Name] {
				continue
			}
			seen[t.Name] = true
			tmplOpts = append(tmplOpts, huh.NewOption(t.Name, t.Name))
		}
		if cfg.DefaultTemplate == "" {
			cfg.DefaultTemplate = templates[0].Name
		}
		fields = append(fields, huh.NewSelect[string]().
			Title("Default template").Options(tmplOpts...).Value(&cfg.DefaultTemplate))
	}
	// The offered models depend on the host's vendor — an Intel model can't start
	// on an AMD host. Best-effort: an unreadable vendor just means the choices are
	// the vendor-neutral ones.
	cpuOpts := optsFromCPUChoices(config.CPUTypeChoices(pm.NodeCPUVendor(cfg.DefaultNode)))
	if cfg.CPUType == "" {
		cfg.CPUType = config.DefaultCPUType
	}
	fields = append(fields,
		huh.NewSelect[string]().Title("VM CPU model").
			Description("Portable models can live-migrate anywhere but lack PCLMULQDQ, which some binaries require.").
			Options(cpuOpts...).Value(&cfg.CPUType),
		huh.NewInput().Title("Default gateway (optional)").
			Description("Used to pre-fill a static IP when creating a VM.").
			Placeholder("192.168.1.1").Value(&cfg.DefaultGateway),
		huh.NewInput().Title("Subnet prefix / CIDR").Placeholder("24").
			Value(&cidrStr).Validate(validateOptionalCIDR),
		huh.NewInput().Title("Dotfiles repo (optional, SSH URL)").
			Description("Enables the dotfiles software option; empty keeps it hidden.").
			Placeholder("git@github.com:you/dotfiles.git").Value(&cfg.DotfilesRepo),
	)
	if err := huh.NewForm(huh.NewGroup(fields...)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return err
	}
	cfg.DefaultCIDR = parseCIDROr(cidrStr, 24)

	// Step 4 — SSH keys (needed so cloud-init can grant you passwordless login).
	if err := pickSSHKeys(cfg); err != nil {
		return err
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Printf("✓ Configuration saved to %s\n", p)
	return nil
}

func runSetupNonInteractive() error {
	cfg := &config.Config{}
	if config.Exists() {
		if c, err := config.Load(); err == nil {
			cfg = c
		}
	}
	cfg.ProxmoxURL = setupURL
	if setupTokenID != "" {
		cfg.TokenID = setupTokenID
	}
	if setupTokenSecret != "" {
		cfg.TokenSecret = setupTokenSecret
	}
	cfg.Insecure = setupInsecure
	if setupNode != "" {
		cfg.DefaultNode = setupNode
	}
	if setupStorage != "" {
		cfg.DefaultStorage = setupStorage
	}
	if setupBridge != "" {
		cfg.DefaultBridge = setupBridge
	}
	if setupGateway != "" {
		cfg.DefaultGateway = setupGateway
	}
	if setupCIDR != 0 {
		cfg.DefaultCIDR = setupCIDR
	}
	if setupTemplate != "" {
		cfg.DefaultTemplate = setupTemplate
	}
	if setupCPUType != "" {
		cfg.CPUType = setupCPUType
	}
	if setupDotfilesRepo != "" {
		cfg.DotfilesRepo = setupDotfilesRepo
	}

	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	if err := pm.Ping(); err != nil {
		return fmt.Errorf("could not reach Proxmox: %w", err)
	}
	if nodes, err := pm.Nodes(); err == nil {
		cfg.Nodes = cfg.Nodes[:0]
		for _, n := range nodes {
			cfg.Nodes = append(cfg.Nodes, n.Name)
		}
		if cfg.DefaultNode == "" && len(nodes) > 0 {
			cfg.DefaultNode = nodes[0].Name
		}
	}

	// Resolve requested SSH keys from ~/.ssh.
	if len(setupSSHKeys) > 0 {
		scanned, err := config.ScanSSHKeys()
		if err != nil {
			return err
		}
		cfg.SSHKeys = cfg.SSHKeys[:0]
		for _, k := range scanned {
			if slices.Contains(setupSSHKeys, k.Name) {
				cfg.SSHKeys = append(cfg.SSHKeys, k)
			}
		}
		if cfg.DefaultSSHKey == "" && len(cfg.SSHKeys) > 0 {
			cfg.DefaultSSHKey = cfg.SSHKeys[0].Name
		}
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Printf("✓ Configuration saved to %s\n", p)
	return nil
}

func runSetupAdd() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cmdPalette = theme.Get(cfg.Theme)
	if setupAddNode != "" {
		if !slices.Contains(cfg.Nodes, setupAddNode) {
			cfg.Nodes = append(cfg.Nodes, setupAddNode)
			fmt.Printf("✓ Added node %q\n", setupAddNode)
		}
	}
	if setupAddSSHKey {
		if err := pickSSHKeys(cfg); err != nil {
			return err
		}
	}
	if setupDotfilesRepo != "" {
		cfg.DotfilesRepo = setupDotfilesRepo
		fmt.Printf("✓ Set dotfiles repo to %s\n", setupDotfilesRepo)
	}
	if setupCPUType != "" {
		cfg.CPUType = setupCPUType
		fmt.Printf("✓ Set VM CPU model to %s\n", setupCPUType)
	}
	return cfg.Save()
}

// pickSSHKeys scans ~/.ssh, lets the user choose which public keys hlab may use,
// and which one is the default.
func pickSSHKeys(cfg *config.Config) error {
	scanned, err := config.ScanSSHKeys()
	if err != nil {
		return err
	}
	if len(scanned) == 0 {
		fmt.Println("! No public keys found in ~/.ssh — you can add one later with `hlab setup --add-ssh-key`.")
		return nil
	}
	opts := make([]huh.Option[string], 0, len(scanned))
	for _, k := range scanned {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", k.Name, k.Path), k.Name))
	}
	var chosen []string
	for _, k := range cfg.SSHKeys {
		chosen = append(chosen, k.Name)
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("SSH public keys").
			Description("Selected keys can be injected into new VMs via cloud-init for passwordless SSH.").
			Options(opts...).
			Value(&chosen),
	)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return err
	}
	// Merge chosen keys into config.
	cfg.SSHKeys = cfg.SSHKeys[:0]
	for _, k := range scanned {
		if slices.Contains(chosen, k.Name) {
			cfg.SSHKeys = append(cfg.SSHKeys, k)
		}
	}
	if cfg.DefaultSSHKey == "" && len(cfg.SSHKeys) > 0 {
		cfg.DefaultSSHKey = cfg.SSHKeys[0].Name
	}
	return nil
}

func optsFromStorages(ss []proxmox.Storage, current *string, fallback string) []huh.Option[string] {
	var opts []huh.Option[string]
	found := false
	for _, s := range ss {
		opts = append(opts, huh.NewOption(s.Name, s.Name))
		if s.Name == *current {
			found = true
		}
	}
	if len(opts) == 0 {
		opts = append(opts, huh.NewOption(fallback, fallback))
	}
	if *current == "" || !found {
		*current = opts[0].Value
	}
	return opts
}

// optsFromCPUChoices renders the curated CPU models as select options. The
// trade-off rides in the label, since huh shows a Description once for the whole
// select, not per option — and the trade-off is the entire point of the choice.
func optsFromCPUChoices(choices []config.CPUChoice) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(choices))
	for _, c := range choices {
		opts = append(opts, huh.NewOption(c.Label+" — "+c.Desc, c.Value))
	}
	return opts
}

func optsFromStrings(values []string, current *string, fallback string) []huh.Option[string] {
	var opts []huh.Option[string]
	found := false
	for _, v := range values {
		opts = append(opts, huh.NewOption(v, v))
		if v == *current {
			found = true
		}
	}
	if len(opts) == 0 {
		opts = append(opts, huh.NewOption(fallback, fallback))
	}
	if *current == "" || !found {
		*current = opts[0].Value
	}
	return opts
}

func validateOptionalCIDR(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 32 {
		return fmt.Errorf("enter a prefix between 1 and 32 (e.g. 24)")
	}
	return nil
}

func parseCIDROr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 32 {
		return n
	}
	return def
}
