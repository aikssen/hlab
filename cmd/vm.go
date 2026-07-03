package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/plans"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/software"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
	"github.com/aikssen/hlab/internal/theme"
	"github.com/aikssen/hlab/internal/wizard"
)

// cmdPalette is the active color theme for CLI output (result/summary boxes,
// plan status). It defaults to the built-in default and is set from the loaded
// config by loadStack, so every color literal in cmd flows from the palette.
var cmdPalette = theme.Get("")

var (
	createDryRun bool

	// Non-interactive create flags (skip the wizard when --name is set).
	cName       string
	cNode       string
	cTemplate   string
	cTemplateID int
	cVMID       int
	cPlan       string
	cCores      int
	cMemoryGB   int
	cDiskGB     int
	cDHCP       bool
	cIP         string
	cGateway    string
	cDNS        []string
	cUser       string
	cPassword   string
	cSSHKey     string

	// Provision flags (non-interactive); selection lives in `provision`.
	pSoftware []string
)

var vmCmd = &cobra.Command{
	Use:   "vm",
	Short: "Create and manage VMs",
}

var vmCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a VM interactively (wizard)",
	RunE:  runVMCreate,
}

var vmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List VMs hlab manages (with provisioned software)",
	RunE:  runVMList,
}

var vmShowCmd = &cobra.Command{
	Use:   "show <name|id>",
	Short: "Show a VM's details and what was provisioned",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMShow,
}

var destroyYes bool

var vmDestroyCmd = &cobra.Command{
	Use:   "destroy <name|id>",
	Short: "Destroy a VM and remove its declaration",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMDestroy,
}

var vmProvisionCmd = &cobra.Command{
	Use:   "provision <name|id>",
	Short: "Provision a VM with its selected software (Ansible)",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMProvision,
}

var vmSSHCmd = &cobra.Command{
	Use:   "ssh <name|id>",
	Short: "SSH into a VM",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMSSH,
}

var vmStartCmd = &cobra.Command{
	Use:   "start <name|id>",
	Short: "Start (power on) a VM",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMStart,
}

var stopForce bool

var vmStopCmd = &cobra.Command{
	Use:   "stop <name|id>",
	Short: "Stop a VM (graceful shutdown; --force for a hard stop)",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMStop,
}

var vmRebootCmd = &cobra.Command{
	Use:   "reboot <name|id>",
	Short: "Reboot a VM (graceful guest reboot)",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMReboot,
}

var migrateToNode string

var vmMigrateCmd = &cobra.Command{
	Use:   "migrate <name|id> --to <node>",
	Short: "Migrate a VM to another cluster node (keeps disk and VM id)",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMMigrate,
}

var (
	snapDescription string
	snapRAM         bool
	snapYes         bool
)

var (
	rCores  int
	rMemGB  int
	rDiskGB int
	rPlan   string
)

var vmResizeCmd = &cobra.Command{
	Use:   "resize <name|id>",
	Short: "Change an existing VM's CPU / RAM / disk (disk grows only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMResize,
}

var vmSnapshotCmd = &cobra.Command{
	Use:   "snapshot <name|id> <snapname>",
	Short: "Create a snapshot of a VM",
	Args:  cobra.ExactArgs(2),
	RunE:  runVMSnapshot,
}

var vmSnapshotsCmd = &cobra.Command{
	Use:   "snapshots <name|id>",
	Short: "List a VM's snapshots",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMSnapshots,
}

var vmRollbackCmd = &cobra.Command{
	Use:   "rollback <name|id> <snapname>",
	Short: "Roll a VM back to a snapshot (discards changes since)",
	Args:  cobra.ExactArgs(2),
	RunE:  runVMRollback,
}

var vmSnapshotDeleteCmd = &cobra.Command{
	Use:   "snapshot-delete <name|id> <snapname>",
	Short: "Delete a VM snapshot",
	Args:  cobra.ExactArgs(2),
	RunE:  runVMSnapshotDelete,
}

func init() {
	f := vmCreateCmd.Flags()
	f.BoolVar(&createDryRun, "dry-run", false, "show the terraform plan without applying")
	f.StringVar(&cName, "name", "", "VM name/hostname (setting this skips the wizard)")
	f.StringVar(&cNode, "node", "", "cluster node to create on (default: default_node from config, else the node holding the template)")
	f.StringVar(&cTemplate, "template", "", "template name to clone (default: configured default template)")
	f.IntVar(&cTemplateID, "template-id", 0, "template VM id to clone (overrides --template)")
	f.IntVar(&cVMID, "vmid", 0, "VM id")
	f.StringVar(&cPlan, "plan", "", "preconfigured plan (e.g. KVM2); overrides --cores/--memory/--disk")
	f.IntVar(&cCores, "cores", 2, "CPU cores")
	f.IntVar(&cMemoryGB, "memory", 4, "memory in GB")
	f.IntVar(&cDiskGB, "disk", 16, "disk size in GB (bumped up to the template size if smaller)")
	f.BoolVar(&cDHCP, "dhcp", true, "use DHCP (set --dhcp=false for static)")
	f.StringVar(&cIP, "ip", "", "static IPv4 with CIDR, e.g. 192.168.1.50/24")
	f.StringVar(&cGateway, "gateway", "", "static gateway")
	f.StringSliceVar(&cDNS, "dns", nil, "DNS servers (static)")
	f.StringVar(&cUser, "user", config.DefaultUsername(), "administrative username")
	f.StringVar(&cPassword, "password", "", "cloud-init password (required — guarantees a login method)")
	f.StringVar(&cSSHKey, "ssh-key", "", "SSH key name to inject (default: configured default)")

	pf := vmProvisionCmd.Flags()
	pf.StringSliceVar(&pSoftware, "software", nil, "software keys to install (skips the prompt; include 'dotfiles' for the terminal environment)")

	vmDestroyCmd.Flags().BoolVarP(&destroyYes, "yes", "y", false, "skip the confirmation prompt")

	vmStopCmd.Flags().BoolVar(&stopForce, "force", false, "hard stop (cut power) instead of a graceful shutdown")

	vmMigrateCmd.Flags().StringVar(&migrateToNode, "to", "", "target node to migrate the VM to (required)")

	sf := vmSnapshotCmd.Flags()
	sf.StringVar(&snapDescription, "description", "", "snapshot description")
	sf.BoolVar(&snapRAM, "ram", false, "include the VM's RAM (live state; VM must be running)")
	vmRollbackCmd.Flags().BoolVarP(&snapYes, "yes", "y", false, "skip the confirmation prompt")
	vmSnapshotDeleteCmd.Flags().BoolVarP(&snapYes, "yes", "y", false, "skip the confirmation prompt")

	rf := vmResizeCmd.Flags()
	rf.IntVar(&rCores, "cores", 0, "new CPU core count")
	rf.IntVar(&rMemGB, "memory", 0, "new memory in GB")
	rf.IntVar(&rDiskGB, "disk", 0, "new disk size in GB (can only grow)")
	rf.StringVar(&rPlan, "plan", "", "apply a plan's cores/memory/disk (e.g. KVM4)")

	vmCmd.AddCommand(vmCreateCmd, vmListCmd, vmShowCmd, vmDestroyCmd, vmProvisionCmd, vmSSHCmd, vmStartCmd, vmStopCmd, vmRebootCmd, vmMigrateCmd,
		vmSnapshotCmd, vmSnapshotsCmd, vmRollbackCmd, vmSnapshotDeleteCmd, vmResizeCmd)
	rootCmd.AddCommand(vmCmd)
}

// loadStack loads config and builds the state store and terraform runner.
func loadStack() (*config.Config, *state.Store, *terraform.Runner, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, err
	}
	cmdPalette = theme.Get(cfg.Theme)
	store := state.New(cfg.StateDirExpanded())
	if err := store.Init(); err != nil {
		return nil, nil, nil, err
	}
	runner := terraform.New(store.TerraformDir(), cfg)
	runner.Verbose = verbosity
	return cfg, store, runner, nil
}

// newEngine builds the discovery client + presentation-free engine from an
// already-loaded stack. Centralizes the `proxmox.New(...) + engine.New(...)` pair
// every command repeated inline.
func newEngine(cfg *config.Config, store *state.Store, runner *terraform.Runner) *engine.Engine {
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	return engine.New(cfg, store, runner, pm)
}

// runStep runs a long operation. In quiet mode (the default) it shows an animated
// spinner with the given title; with -v the operation's own output is streamed
// instead, so no spinner is used.
func runStep(title string, fn func() error) error {
	if verbosity > 0 {
		return fn()
	}
	// A spinner only makes sense on an interactive terminal. When stdout is
	// redirected (piped/captured, e.g. `hlab … 2>&1 | tail`) the animation would
	// flood the stream with hundreds of braille frames and control sequences, so
	// print the title once and run the step directly. Either path runs fn and
	// returns its error unchanged; the caller prints the ✓/error line.
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		fmt.Printf("… %s\n", title)
		return fn()
	}
	var err error
	_ = spinner.New().Title(" " + title).Action(func() { err = fn() }).Run()
	return err
}

// resolveVMName accepts a VM name or a numeric VM ID and returns the canonical
// declaration name.
func resolveVMName(store *state.Store, arg string) (string, error) {
	if _, err := strconv.Atoi(arg); err == nil {
		vms, lerr := store.List()
		if lerr != nil {
			return "", lerr
		}
		for _, vm := range vms {
			if strconv.Itoa(vm.VMID) == arg {
				return vm.Name, nil
			}
		}
		return "", fmt.Errorf("no managed VM with id %s", arg)
	}
	if _, err := store.Load(arg); err != nil {
		return "", fmt.Errorf("no managed VM named %q", arg)
	}
	return arg, nil
}

func runVMCreate(cmd *cobra.Command, _ []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}

	// The --user flag default (config.DefaultUsername) is fixed at registration,
	// before config loads. When the user didn't set it explicitly, resolve the
	// create default from config (last-used username, else the OS user).
	if !cmd.Flags().Changed("user") {
		cUser = cfg.CreateUserDefault()
	}

	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	if err := pm.Ping(); err != nil {
		return fmt.Errorf("cannot reach Proxmox: %w (run `hlab doctor`)", err)
	}

	// Suggest a static IP in the configured gateway's subnet, skipping addresses
	// already assigned to managed VMs.
	used := map[string]bool{}
	if existing, lerr := store.List(); lerr == nil {
		for _, vm := range existing {
			if vm.IPCIDR != "" {
				used[strings.SplitN(vm.IPCIDR, "/", 2)[0]] = true
			}
		}
	}
	suggestedIP := cfg.SuggestIPCIDR(used)

	var res *wizard.Result
	if cName != "" {
		res, err = buildResultFromFlags(cfg, pm, suggestedIP)
	} else {
		res, err = wizard.Run(cfg, pm, suggestedIP)
	}
	if err != nil {
		return err
	}
	if res == nil {
		fmt.Println("Cancelled.")
		return nil
	}

	eng := engine.New(cfg, store, runner, pm)

	if createDryRun {
		fmt.Println("\n--- terraform plan (dry-run) ---")
		return eng.DryRun(res)
	}

	var ip string
	if err := runStep(
		fmt.Sprintf("Creating %s with Terraform… (-v for details)", res.VM.Name),
		func() error { var e error; ip, e = eng.Create(res); return e },
	); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(renderResult("✓ VM created", res.VM, ip))
	fmt.Println("\n  Next steps:")
	fmt.Printf("    hlab vm provision %d    # install software / dotfiles\n", res.VM.VMID)
	fmt.Printf("    hlab vm ssh %d           # connect to the VM\n", res.VM.VMID)
	return nil
}

// renderResult draws a summary box for a VM operation.
func renderResult(title string, vm *state.VMSpec, ip string) string {
	rows := [][2]string{
		{"Name", vm.Name},
		{"ID / Node", fmt.Sprintf("%d  %s", vm.VMID, vm.Node)},
	}
	if vm.Plan != "" {
		rows = append(rows, [2]string{"Plan", vm.Plan})
	}
	rows = append(rows,
		[2]string{"CPU / RAM", fmt.Sprintf("%d cores / %s", vm.Cores, ramDisplay(vm))},
		[2]string{"Disk", fmt.Sprintf("%d GB", vm.DiskGB)},
		[2]string{"IP", ipOrDash(ip)},
		[2]string{"User", vm.Username},
		[2]string{"Login", loginDesc(vm)},
	)
	if vm.IsLXC() {
		rows = append(rows, [2]string{"Unprivileged", yesNo(vm.Unprivileged)}, [2]string{"Nesting", yesNo(vm.Nesting)})
	}
	if len(vm.Software) > 0 {
		rows = append(rows, [2]string{"Software", strings.Join(vm.Software, ", ")})
	}
	// Size the label column to the longest label present (plus a gap) so no label
	// wraps onto its own line — "Unprivileged" (LXC) is wider than the old fixed 11.
	labelWidth := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > labelWidth {
			labelWidth = w
		}
	}
	label := lipgloss.NewStyle().Foreground(cmdPalette.Accent).Width(labelWidth + 2)
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(title))
	b.WriteString("\n\n")
	for _, r := range rows {
		b.WriteString(label.Render(r[0]))
		b.WriteString(r[1])
		b.WriteByte('\n')
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cmdPalette.Good).
		Padding(0, 2).
		Render(strings.TrimRight(b.String(), "\n"))
}

// ramDisplay formats a guest's RAM for display, unit included: LXC always
// shows MB (matching `ct list`'s RAM(MB) column); VMs show whole GB via
// ramGBDisplay, falling back to a decimal-GB rendering of MemoryMB when
// MemoryGB is unset — an adopted VM can have RAM that isn't a whole number
// of gigabytes (e.g. 2560 MB).
func ramDisplay(vm *state.VMSpec) string {
	if vm.IsLXC() {
		return fmt.Sprintf("%d MB", vm.MemoryMB)
	}
	return ramGBDisplay(vm) + " GB"
}

// ramGBDisplay formats a VM's RAM in GB with no unit suffix (for tabular
// columns whose header already says "GB"), falling back to MemoryMB —
// rendered as a decimal GB value, e.g. "2.5" — when MemoryGB is unset.
func ramGBDisplay(vm *state.VMSpec) string {
	if vm.MemoryGB > 0 {
		return strconv.Itoa(vm.MemoryGB)
	}
	if vm.MemoryMB > 0 {
		return strconv.FormatFloat(float64(vm.MemoryMB)/1024, 'f', -1, 64)
	}
	return "0"
}

func ipOrDash(ip string) string {
	if ip == "" {
		return "(pending — run `hlab vm list`)"
	}
	return ip
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func loginDesc(vm *state.VMSpec) string {
	switch {
	case vm.HasPassword && len(vm.SSHKeys) > 0:
		return "password + ssh key"
	case vm.HasPassword:
		return "password"
	default:
		return "ssh key"
	}
}

// buildResultFromFlags assembles a VM declaration from CLI flags (non-interactive
// create), resolving the template id from --template when needed.
// pickNode chooses the node a guest is created on from the candidate nodes that
// actually hold the requested template (with node-local storage a clone/rootfs
// must land on the node that has the template). Precedence, for least surprise:
//  1. an explicit --node (must be one of the candidates),
//  2. the configured default_node (when it is a candidate),
//  3. the first candidate found during discovery (fallback).
func pickNode(explicit, defaultNode string, candidates []string) (string, error) {
	if explicit != "" {
		if slices.Contains(candidates, explicit) {
			return explicit, nil
		}
		return "", fmt.Errorf("node %q does not hold the requested template (available on: %s)",
			explicit, strings.Join(candidates, ", "))
	}
	if defaultNode != "" && slices.Contains(candidates, defaultNode) {
		return defaultNode, nil
	}
	return candidates[0], nil
}

func buildResultFromFlags(cfg *config.Config, pm *proxmox.Client, suggestedIP string) (*wizard.Result, error) {
	// Storage is configured once in `hlab setup` (like the default bridge); there
	// is no per-VM storage flag.
	storage := cfg.DefaultStorage

	// Resolve the template across all nodes. The same template (by name) can exist
	// on several nodes, so collect every match; the node is then chosen by pickNode
	// (--node, else default_node, else the first match) — VM ids are cluster-unique
	// and with node-local storage a clone must land on a node that holds the template.
	if cTemplateID == 0 && cTemplate == "" {
		cTemplate = cfg.DefaultTemplate
	}
	if cTemplateID == 0 && cTemplate == "" {
		return nil, fmt.Errorf("provide --template or --template-id (or set a default template in `hlab setup`)")
	}
	nodes, err := pm.Nodes()
	if err != nil {
		return nil, err
	}
	var matches []proxmox.Template
	for _, n := range nodes {
		ts, _ := pm.Templates(n.Name)
		for _, t := range ts {
			if (cTemplateID != 0 && t.VMID == cTemplateID) || (cTemplateID == 0 && t.Name == cTemplate) {
				matches = append(matches, t)
			}
		}
	}
	if len(matches) == 0 {
		if cTemplateID != 0 {
			return nil, fmt.Errorf("template id %d not found in the cluster", cTemplateID)
		}
		return nil, fmt.Errorf("template %q not found in the cluster", cTemplate)
	}
	candidateNodes := make([]string, len(matches))
	for i, t := range matches {
		candidateNodes[i] = t.Node
	}
	node, err := pickNode(cNode, cfg.DefaultNode, candidateNodes)
	if err != nil {
		return nil, err
	}
	var tmpl proxmox.Template
	for _, t := range matches {
		if t.Node == node {
			tmpl = t
			break
		}
	}
	templateID, templateName := tmpl.VMID, tmpl.Name

	if cVMID == 0 {
		return nil, fmt.Errorf("--vmid is required")
	}

	// A preconfigured plan overrides cores/memory/disk.
	cores, memGB, diskGB, planName := cCores, cMemoryGB, cDiskGB, ""
	if cPlan != "" {
		cat, lerr := plans.Load()
		if lerr != nil {
			return nil, lerr
		}
		p, ok := plans.ByName(cat, cPlan)
		if !ok {
			return nil, fmt.Errorf("unknown plan %q (edit ~/.hlab/plans.yaml)", cPlan)
		}
		cores, memGB, diskGB, planName = p.Cores, p.MemoryGB, p.DiskGB, p.Name
	}

	// The clone cannot shrink the template's disk; bump up if it is smaller.
	if tmplGB, _ := pm.TemplateDiskGB(node, templateID); tmplGB > 0 && diskGB < tmplGB {
		fmt.Printf("• disk %d GB is smaller than the template (%d GB); using %d GB\n", diskGB, tmplGB, tmplGB)
		diskGB = tmplGB
	}
	if cPassword == "" {
		return nil, fmt.Errorf("--password is required (it guarantees a login method); an --ssh-key is optional and additive")
	}

	vm := &state.VMSpec{
		Name:        cName,
		Node:        node,
		VMID:        cVMID,
		Template:    templateName,
		TemplateID:  templateID,
		Storage:     storage,
		Bridge:      cfg.DefaultBridge,
		Plan:        planName,
		Cores:       cores,
		MemoryGB:    memGB,
		DiskGB:      diskGB,
		DHCP:        cDHCP,
		Username:    cUser,
		HasPassword: cPassword != "",
	}
	if !cDHCP {
		ip := cIP
		if ip == "" {
			ip = suggestedIP
		}
		gw := cGateway
		if gw == "" {
			gw = cfg.DefaultGateway
		}
		if ip == "" || gw == "" {
			return nil, fmt.Errorf("static networking requires --ip and --gateway (or set defaults via `hlab setup`)")
		}
		vm.IPCIDR = ip
		vm.Gateway = gw
		vm.DNS = cDNS
	}
	keyName := cSSHKey
	if keyName == "" {
		keyName = cfg.DefaultSSHKey
	}
	if keyName != "" {
		pub, ok := cfg.SSHKeyByName(keyName)
		if !ok {
			return nil, fmt.Errorf("ssh key %q not found in config", keyName)
		}
		vm.SSHKeys = []string{pub}
	}
	return &wizard.Result{VM: vm, Password: cPassword}, nil
}

func runVMProvision(cmd *cobra.Command, args []string) error {
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

	// Choose what to install: flags (non-interactive) or an interactive prompt
	// defaulting to whatever the declaration already records.
	if cmd.Flags().Changed("software") {
		vm.Software = pSoftware
	} else {
		sw, serr := wizard.ProvisionOptions(cmdHuhTheme(), vm.Software, cfg.DotfilesRepo != "")
		if serr != nil {
			return serr
		}
		vm.Software = sw
	}

	if len(vm.Software) == 0 {
		fmt.Println("Nothing to provision (no software selected).")
		return nil
	}

	eng := newEngine(cfg, store, runner)
	eng.AnsibleVerbose = verbosity

	ip := eng.ResolveIP(vm)

	// Installing dotfiles from a private repo relies on a forwarded SSH agent.
	if slices.Contains(vm.Software, software.DotfilesKey) && !sshAgentHasKeys() {
		fmt.Println("! dotfiles is selected but your SSH agent has no keys.")
		fmt.Println("  If the dotfiles repo is private, load the key GitHub authorizes first, e.g.:")
		fmt.Println("    ssh-add ~/.ssh/id_ed25519")
	}

	fmt.Printf("Provisioning %q (%s) with: %s\n", name, ip, strings.Join(vm.Software, ", "))
	if err := runStep(
		"Running Ansible… (can take several minutes; -v for details)",
		func() error { return eng.Provision(vm) },
	); err != nil {
		return err
	}
	fmt.Printf("\n✓ Provisioned %q.\n", name)
	return nil
}

// sshAgentHasKeys reports whether the local SSH agent has at least one identity.
func sshAgentHasKeys() bool {
	out, err := exec.Command("ssh-add", "-l").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0 &&
		!strings.Contains(string(out), "no identities")
}

func runVMSSH(_ *cobra.Command, args []string) error {
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
	ip := newEngine(cfg, store, runner).ResolveIP(vm)
	if ip == "" {
		return fmt.Errorf("no IP address known for %q yet", name)
	}
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found in PATH")
	}
	// Replace this process with an interactive ssh session.
	return syscall.Exec(bin, []string{"ssh", fmt.Sprintf("%s@%s", vm.Username, ip)}, os.Environ())
}

func runVMStart(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)
	if err := eng.Start(name); err != nil {
		return err
	}
	fmt.Printf("✓ Started %q.\n", name)
	return nil
}

func runVMStop(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)
	if err := eng.Stop(name, stopForce); err != nil {
		return err
	}
	if stopForce {
		fmt.Printf("✓ Stopped %q (hard).\n", name)
	} else {
		fmt.Printf("✓ Shutdown requested for %q (graceful).\n", name)
	}
	return nil
}

func runVMReboot(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)
	if err := eng.Reboot(name); err != nil {
		return err
	}
	fmt.Printf("✓ Reboot requested for %q.\n", name)
	return nil
}

func runVMMigrate(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	if migrateToNode == "" {
		return fmt.Errorf("specify the target node with --to (e.g. --to pve2)")
	}
	vm, err := store.Load(name)
	if err != nil {
		return err
	}
	if vm.Node == migrateToNode {
		return fmt.Errorf("%q is already on node %q", name, migrateToNode)
	}

	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Migrating %s from %s to %s… (-v for details)", name, vm.Node, migrateToNode),
		func() error { return eng.Migrate(name, migrateToNode) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ Migrated %q to %s.\n", name, migrateToNode)
	return nil
}

func runVMResize(cmd *cobra.Command, args []string) error {
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

	// Apply a plan first (it sets all three), then let explicit flags override — an
	// explicit value means the spec no longer matches a named plan, so mark it custom.
	changed := false
	if cmd.Flags().Changed("plan") {
		cat, lerr := plans.Load()
		if lerr != nil {
			return lerr
		}
		p, ok := plans.ByName(cat, rPlan)
		if !ok {
			return fmt.Errorf("unknown plan %q (edit ~/.hlab/plans.yaml)", rPlan)
		}
		vm.Plan, vm.Cores, vm.MemoryGB, vm.MemoryMB, vm.DiskGB = p.Name, p.Cores, p.MemoryGB, 0, p.DiskGB
		changed = true
	}
	if cmd.Flags().Changed("cores") {
		vm.Cores, vm.Plan, changed = rCores, "", true
	}
	if cmd.Flags().Changed("memory") {
		// Clear MemoryMB too: an adopted VM may have a non-GB MemoryMB value
		// (e.g. 2560), which writeTfvars prefers over MemoryGB when set — leaving
		// it around would make an explicit --memory resize a silent no-op.
		vm.MemoryGB, vm.MemoryMB, vm.Plan, changed = rMemGB, 0, "", true
	}
	if cmd.Flags().Changed("disk") {
		vm.DiskGB, vm.Plan, changed = rDiskGB, "", true
	}
	if !changed {
		return fmt.Errorf("nothing to change — pass --cores, --memory, --disk or --plan")
	}

	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Reconfiguring %s with Terraform… (-v for details)", name),
		func() error { return eng.Reconfigure(vm) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ Reconfigured %q: %d cores / %d GB RAM / %d GB disk.\n", name, vm.Cores, vm.MemoryGB, vm.DiskGB)
	fmt.Println("  (CPU/RAM changes may need a reboot; growing the filesystem to fill a larger disk happens on reboot via cloud-init growpart, or resize it manually.)")
	return nil
}

func runVMSnapshot(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	snap := args[1]
	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Creating snapshot %q of %s…", snap, name),
		func() error { return eng.Snapshot(name, snap, snapDescription, snapRAM) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ Snapshot %q created for %q.\n", snap, name)
	return nil
}

func runVMSnapshots(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)
	snaps, err := eng.Snapshots(name)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Printf("No snapshots for %q.\n", name)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tWHEN\tRAM\tDESCRIPTION")
	for _, s := range snaps {
		ram := "no"
		if s.WithRAM {
			ram = "yes"
		}
		when := "—"
		if s.Time > 0 {
			when = time.Unix(s.Time, 0).Format("2006-01-02 15:04")
		}
		desc := s.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, when, ram, desc)
	}
	return w.Flush()
}

func runVMRollback(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	snap := args[1]
	if !snapYes {
		confirm := false
		if err := confirmf(&confirm, "Roll back %q to snapshot %q? Changes since the snapshot are lost.", name, snap); err != nil {
			return err
		}
		if !confirm {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Rolling back %s to %q…", name, snap),
		func() error { return eng.RollbackSnapshot(name, snap) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ %q rolled back to %q.\n", name, snap)
	return nil
}

func runVMSnapshotDelete(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	snap := args[1]
	if !snapYes {
		confirm := false
		if err := confirmf(&confirm, "Delete snapshot %q of %q?", snap, name); err != nil {
			return err
		}
		if !confirm {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Deleting snapshot %q of %s…", snap, name),
		func() error { return eng.DeleteSnapshot(name, snap) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ Snapshot %q deleted for %q.\n", snap, name)
	return nil
}

func runVMShow(_ *cobra.Command, args []string) error {
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
	ip := newEngine(cfg, store, runner).ResolveIP(vm)
	fmt.Println(renderResult("VM "+name, vm, ip))
	if len(vm.Software) == 0 {
		fmt.Printf("\n  Not provisioned yet — run `hlab vm provision %d`\n", vm.VMID)
	}
	return nil
}

func runVMList(_ *cobra.Command, _ []string) error {
	_, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	all, err := store.List()
	if err != nil {
		return err
	}
	var vms []*state.VMSpec
	for _, vm := range all {
		if !vm.IsLXC() {
			vms = append(vms, vm)
		}
	}
	if len(vms) == 0 {
		fmt.Println("No VMs yet. Create one with `hlab vm create`.")
		return nil
	}
	ips := runner.IPAddresses()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tNODE\tCPU\tRAM(GB)\tDISK(GB)\tIP\tPROVISIONED")
	for _, vm := range vms {
		ip := engine.DeclaredIP(vm) // static IP is authoritative
		if ip == "" {
			ip = engine.FirstIPv4(ips[vm.Name])
		}
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%d\t%s\t%s\n",
			vm.Name, vm.VMID, vm.Node, vm.Cores, ramGBDisplay(vm), vm.DiskGB, ip, provisionedDesc(vm))
	}
	return w.Flush()
}

// provisionedDesc summarizes a guest's provisioning selection for list output.
func provisionedDesc(vm *state.VMSpec) string {
	if len(vm.Software) == 0 {
		return "-"
	}
	return strings.Join(vm.Software, ",")
}

func runVMDestroy(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}

	if !destroyYes {
		confirm := false
		if err := confirmf(&confirm, "Destroy VM %q? This deletes the VM in Proxmox.", name); err != nil {
			return err
		}
		if !confirm {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Destroying %s… (-v for details)", name),
		func() error { return eng.Destroy(name) },
	); err != nil {
		return err
	}

	fmt.Printf("✓ VM %q destroyed.\n", name)
	return nil
}
