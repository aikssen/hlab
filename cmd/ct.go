package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/plans"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/wizard"
)

var (
	// Non-interactive `ct create` flags (skip the wizard when --name is set).
	ctName         string
	ctNode         string
	ctTemplateFile string
	ctOSType       string
	ctVMID         int
	ctPlan         string
	ctCores        int
	ctMem          string
	ctDiskGB       int
	ctSwapMB       int
	ctUnpriv       bool
	ctDHCP         bool
	ctIP           string
	ctGateway      string
	ctDNS          []string
	ctPassword     string
	ctSSHKey       string

	// `ct resize` flags.
	ctrCores  int
	ctrMem    string
	ctrDiskGB int
	ctrSwap   int
	ctrPlan   string
)

var ctCmd = &cobra.Command{
	Use:   "ct",
	Short: "Create and manage LXC containers",
	Long: "Create and manage LXC containers.\n\n" +
		"Containers are lighter than VMs (shared kernel, no BIOS/agent) and are a good " +
		"fit for system services (DNS, reverse proxy, caches). They are created from a " +
		"container template (vztmpl), not cloned from a VM. A static IP is recommended: " +
		"containers have no guest agent, so a DHCP address cannot be auto-discovered.",
}

var ctCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an LXC container interactively (wizard)",
	RunE:  runCTCreate,
}

var ctListCmd = &cobra.Command{
	Use:   "list",
	Short: "List LXC containers hlab manages",
	RunE:  runCTList,
}

var ctShowCmd = &cobra.Command{
	Use:   "show <name|id>",
	Short: "Show a container's details",
	Args:  cobra.ExactArgs(1),
	RunE:  runCTShow,
}

func init() {
	f := ctCreateCmd.Flags()
	f.StringVar(&ctName, "name", "", "container name/hostname (setting this skips the wizard)")
	f.StringVar(&ctNode, "node", "", "cluster node to create on (default: default_node from config, else the node holding the template)")
	f.StringVar(&ctTemplateFile, "template-file", "", "container template volume id (e.g. local:vztmpl/debian-12-standard_...tar.zst)")
	f.StringVar(&ctOSType, "os-type", "", "OS type (debian|ubuntu|alpine|...); inferred from the template when empty")
	f.IntVar(&ctVMID, "vmid", 0, "container id")
	f.StringVar(&ctPlan, "plan", "", "preconfigured LXC plan (micro|small|medium|large); overrides --cores/--memory/--disk")
	f.IntVar(&ctCores, "cores", 1, "CPU cores")
	f.StringVar(&ctMem, "memory", "512M", "memory in GB (e.g. 2), or MB with a suffix (e.g. 512M)")
	f.IntVar(&ctDiskGB, "disk", 4, "rootfs disk size in GB")
	f.IntVar(&ctSwapMB, "swap", 0, "swap in MB (0 = none)")
	f.BoolVar(&ctUnpriv, "unprivileged", true, "create an unprivileged container (recommended)")
	f.BoolVar(&ctDHCP, "dhcp", false, "use DHCP (static is recommended for containers)")
	f.StringVar(&ctIP, "ip", "", "static IPv4 with CIDR, e.g. 192.168.1.60/24")
	f.StringVar(&ctGateway, "gateway", "", "static gateway")
	f.StringSliceVar(&ctDNS, "dns", nil, "DNS servers (static)")
	f.StringVar(&ctPassword, "password", "", "root password (required — Proxmox refuses a passwordless container)")
	f.StringVar(&ctSSHKey, "ssh-key", "", "SSH key name to inject for root (default: configured default)")

	// Provisioning selection reuses the vm provision flags (same catalog).
	pf := ctProvisionCmd.Flags()
	pf.StringSliceVar(&pSoftware, "software", nil, "software keys to install (skips the prompt; include 'dotfiles' for the terminal environment)")

	ctDestroyCmd.Flags().BoolVarP(&destroyYes, "yes", "y", false, "skip the confirmation prompt")
	ctStopCmd.Flags().BoolVar(&stopForce, "force", false, "hard stop (cut power) instead of a graceful shutdown")

	ctSnapshotCmd.Flags().StringVar(&snapDescription, "description", "", "snapshot description")
	ctRollbackCmd.Flags().BoolVarP(&snapYes, "yes", "y", false, "skip the confirmation prompt")
	ctSnapshotDeleteCmd.Flags().BoolVarP(&snapYes, "yes", "y", false, "skip the confirmation prompt")

	crf := ctResizeCmd.Flags()
	crf.IntVar(&ctrCores, "cores", 0, "new CPU core count")
	crf.StringVar(&ctrMem, "memory", "", "new memory in GB (e.g. 2), or MB with a suffix (e.g. 512M)")
	crf.IntVar(&ctrDiskGB, "disk", 0, "new rootfs size in GB (can only grow)")
	crf.IntVar(&ctrSwap, "swap", 0, "new swap in MB (0 = none; independent of the plan)")
	crf.StringVar(&ctrPlan, "plan", "", "apply an LXC plan's cores/memory/disk (micro|small|medium|large)")

	ctMigrateCmd.Flags().StringVar(&migrateToNode, "to", "", "target node to migrate the container to (required)")

	ctCmd.AddCommand(ctCreateCmd, ctListCmd, ctShowCmd, ctStartCmd, ctStopCmd, ctRebootCmd, ctDestroyCmd, ctProvisionCmd, ctSSHCmd,
		ctSnapshotCmd, ctSnapshotsCmd, ctRollbackCmd, ctSnapshotDeleteCmd, ctResizeCmd, ctMigrateCmd)
	rootCmd.AddCommand(ctCmd)
}

// Container verbs that are type-agnostic reuse the vm handlers verbatim — the
// engine routes by the loaded declaration's type, so start/stop/reboot/destroy/
// provision/ssh behave correctly for containers.
var (
	ctStartCmd = &cobra.Command{
		Use: "start <name|id>", Short: "Start (power on) a container",
		Args: cobra.ExactArgs(1), RunE: runVMStart,
	}
	ctStopCmd = &cobra.Command{
		Use: "stop <name|id>", Short: "Stop a container (graceful; --force for a hard stop)",
		Args: cobra.ExactArgs(1), RunE: runVMStop,
	}
	ctRebootCmd = &cobra.Command{
		Use: "reboot <name|id>", Short: "Reboot a container",
		Args: cobra.ExactArgs(1), RunE: runVMReboot,
	}
	ctDestroyCmd = &cobra.Command{
		Use: "destroy <name|id>", Short: "Destroy a container and remove its declaration",
		Args: cobra.ExactArgs(1), RunE: runVMDestroy,
	}
	ctProvisionCmd = &cobra.Command{
		Use: "provision <name|id>", Short: "Provision a container with its selected software (Ansible)",
		Args: cobra.ExactArgs(1), RunE: runVMProvision,
	}
	ctSSHCmd = &cobra.Command{
		Use: "ssh <name|id>", Short: "SSH into a container",
		Args: cobra.ExactArgs(1), RunE: runVMSSH,
	}
	ctSnapshotCmd = &cobra.Command{
		Use: "snapshot <name|id> <snapname>", Short: "Create a snapshot of a container",
		Args: cobra.ExactArgs(2), RunE: runVMSnapshot,
	}
	ctSnapshotsCmd = &cobra.Command{
		Use: "snapshots <name|id>", Short: "List a container's snapshots",
		Args: cobra.ExactArgs(1), RunE: runVMSnapshots,
	}
	ctRollbackCmd = &cobra.Command{
		Use: "rollback <name|id> <snapname>", Short: "Roll a container back to a snapshot (discards changes since)",
		Args: cobra.ExactArgs(2), RunE: runVMRollback,
	}
	ctSnapshotDeleteCmd = &cobra.Command{
		Use: "snapshot-delete <name|id> <snapname>", Short: "Delete a container snapshot",
		Args: cobra.ExactArgs(2), RunE: runVMSnapshotDelete,
	}
	ctResizeCmd = &cobra.Command{
		Use: "resize <name|id>", Short: "Change a container's CPU / RAM / disk (disk grows only)",
		Args: cobra.ExactArgs(1), RunE: runCTResize,
	}
	ctMigrateCmd = &cobra.Command{
		Use: "migrate <name|id> --to <node>", Short: "Migrate a container to another cluster node",
		Args: cobra.ExactArgs(1), RunE: runVMMigrate,
	}
)

func runCTResize(cmd *cobra.Command, args []string) error {
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
	if !vm.IsLXC() {
		return fmt.Errorf("%q is a VM — use `hlab vm resize`", name)
	}

	// Apply a plan first (sets all three), then explicit flags override — an explicit
	// value means the spec no longer matches a named plan, so mark it custom.
	changed := false
	if cmd.Flags().Changed("plan") {
		cat, lerr := plans.LoadLXC()
		if lerr != nil {
			return lerr
		}
		p, ok := plans.ByName(cat, ctrPlan)
		if !ok {
			return fmt.Errorf("unknown LXC plan %q (micro|small|medium|large; edit ~/.hlab/plans.yaml)", ctrPlan)
		}
		vm.Plan, vm.Cores, vm.MemoryMB, vm.DiskGB = p.Name, p.Cores, p.MB(), p.DiskGB
		changed = true
	}
	if cmd.Flags().Changed("cores") {
		vm.Cores, vm.Plan, changed = ctrCores, "", true
	}
	if cmd.Flags().Changed("memory") {
		mb, perr := plans.ParseMem(ctrMem)
		if perr != nil {
			return fmt.Errorf("--memory: %w", perr)
		}
		vm.MemoryMB, vm.Plan, changed = mb, "", true
	}
	if cmd.Flags().Changed("disk") {
		vm.DiskGB, vm.Plan, changed = ctrDiskGB, "", true
	}
	// Swap isn't set by any plan, so changing it neither requires nor invalidates a
	// named plan (vm.Plan is left untouched).
	if cmd.Flags().Changed("swap") {
		vm.SwapMB, changed = ctrSwap, true
	}
	if !changed {
		return fmt.Errorf("nothing to change — pass --cores, --memory, --disk, --swap or --plan")
	}

	eng := newEngine(cfg, store, runner)
	if err := runStep(
		fmt.Sprintf("Reconfiguring %s with Terraform… (-v for details)", name),
		func() error { return eng.Reconfigure(vm) },
	); err != nil {
		return err
	}
	fmt.Printf("✓ Reconfigured %q: %d cores / %d MB RAM / %d MB swap / %d GB disk.\n", name, vm.Cores, vm.MemoryMB, vm.SwapMB, vm.DiskGB)
	return nil
}

func runCTCreate(_ *cobra.Command, _ []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	if err := pm.Ping(); err != nil {
		return fmt.Errorf("cannot reach Proxmox: %w (run `hlab doctor`)", err)
	}

	// Suggest a static IP in the configured gateway's subnet, skipping addresses
	// already assigned to managed guests.
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
	if ctName != "" {
		res, err = buildCTResultFromFlags(cfg, pm, suggestedIP)
	} else {
		res, err = wizard.RunCT(cfg, pm, suggestedIP)
	}
	if err != nil {
		return err
	}
	if res == nil {
		fmt.Println("Cancelled.")
		return nil
	}

	eng := engine.New(cfg, store, runner, pm)
	var ip string
	if err := runStep(
		fmt.Sprintf("Creating container %s with Terraform… (-v for details)", res.VM.Name),
		func() error { var e error; ip, e = eng.Create(res); return e },
	); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(renderResult("✓ Container created", res.VM, ip))
	fmt.Println("\n  Next steps:")
	fmt.Printf("    hlab ct provision %d    # install software / dotfiles\n", res.VM.VMID)
	fmt.Printf("    hlab ct ssh %d           # connect to the container\n", res.VM.VMID)
	return nil
}

func runCTShow(_ *cobra.Command, args []string) error {
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
	fmt.Println(renderResult("Container "+name, vm, ip))
	if len(vm.Software) == 0 {
		fmt.Printf("\n  Not provisioned yet — run `hlab ct provision %d`\n", vm.VMID)
	}
	return nil
}

func runCTList(_ *cobra.Command, _ []string) error {
	cfg, store, _, err := loadStack()
	if err != nil {
		return err
	}
	all, err := store.List()
	if err != nil {
		return err
	}
	var cts []*state.VMSpec
	for _, vm := range all {
		if vm.IsLXC() {
			cts = append(cts, vm)
		}
	}
	if len(cts) == 0 {
		fmt.Println("No containers yet. Create one with `hlab ct create`.")
		return nil
	}
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tNODE\tCPU\tRAM(MB)\tDISK(GB)\tIP\tPROVISIONED")
	for _, vm := range cts {
		ip := engine.DeclaredIP(vm) // static IP is authoritative
		if ip == "" {
			// DHCP container: read the address from the host (no guest agent needed).
			if addrs, aerr := pm.ContainerIPv4s(vm.Node, vm.VMID); aerr == nil {
				ip = engine.FirstIPv4(addrs)
			}
		}
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%d\t%d\t%s\t%s\n",
			vm.Name, vm.VMID, vm.Node, vm.Cores, vm.MemoryMB, vm.DiskGB, ip, provisionedDesc(vm))
	}
	return w.Flush()
}

// buildCTResultFromFlags assembles a container declaration from CLI flags
// (non-interactive create), resolving the template's node from its volume id.
func buildCTResultFromFlags(cfg *config.Config, pm *proxmox.Client, suggestedIP string) (*wizard.Result, error) {
	if ctTemplateFile == "" {
		return nil, fmt.Errorf("provide --template-file (a vztmpl volume id, e.g. local:vztmpl/debian-12-standard_...tar.zst)")
	}
	// The same vztmpl volume id exists on every node that has that (node-local)
	// storage, so collect all nodes holding it and let pickNode choose (--node,
	// else default_node, else the first found). Previously this took the first
	// match, which silently ignored default_node and could land the container on
	// an unexpected node. With node-local storage the rootfs must land on a node
	// that actually holds the template.
	tmpls, err := pm.AllContainerTemplates()
	if err != nil {
		return nil, err
	}
	var candidateNodes []string
	for _, t := range tmpls {
		if t.VolID == ctTemplateFile {
			candidateNodes = append(candidateNodes, t.Node)
		}
	}
	if len(candidateNodes) == 0 {
		return nil, fmt.Errorf("container template %q not found in the cluster", ctTemplateFile)
	}
	node, err := pickNode(ctNode, cfg.DefaultNode, candidateNodes)
	if err != nil {
		return nil, err
	}
	if ctVMID == 0 {
		return nil, fmt.Errorf("--vmid is required")
	}
	osType := ctOSType
	if osType == "" {
		osType = proxmox.OSTypeFromTemplate(ctTemplateFile)
	}

	// A preconfigured plan overrides cores/memory/disk.
	memMB, merr := plans.ParseMem(ctMem)
	if merr != nil {
		return nil, fmt.Errorf("--memory: %w", merr)
	}
	cores, diskGB, planName := ctCores, ctDiskGB, ""
	if ctPlan != "" {
		cat, lerr := plans.LoadLXC()
		if lerr != nil {
			return nil, lerr
		}
		p, ok := plans.ByName(cat, ctPlan)
		if !ok {
			return nil, fmt.Errorf("unknown LXC plan %q (micro|small|medium|large; edit ~/.hlab/plans.yaml)", ctPlan)
		}
		cores, memMB, diskGB, planName = p.Cores, p.MB(), p.DiskGB, p.Name
	}

	if ctPassword == "" {
		return nil, fmt.Errorf("--password is required (Proxmox refuses a passwordless container); an --ssh-key is optional and additive")
	}

	vm := &state.VMSpec{
		Name:         ctName,
		Type:         "lxc",
		Node:         node,
		VMID:         ctVMID,
		Template:     volidDisplay(ctTemplateFile),
		TemplateFile: ctTemplateFile,
		OSType:       osType,
		Storage:      cfg.DefaultStorage,
		Bridge:       cfg.DefaultBridge,
		Plan:         planName,
		Cores:        cores,
		MemoryMB:     memMB,
		DiskGB:       diskGB,
		SwapMB:       ctSwapMB,
		Unprivileged: ctUnpriv,
		Nesting:      true, // always on for hlab-created containers
		DHCP:         ctDHCP,
		Username:     "root",
		HasPassword:  ctPassword != "",
	}
	if !ctDHCP {
		ip := ctIP
		if ip == "" {
			ip = suggestedIP
		}
		gw := ctGateway
		if gw == "" {
			gw = cfg.DefaultGateway
		}
		if ip == "" || gw == "" {
			return nil, fmt.Errorf("static networking requires --ip and --gateway (or set defaults via `hlab setup`)")
		}
		vm.IPCIDR = ip
		vm.Gateway = gw
		vm.DNS = ctDNS
	}
	keyName := ctSSHKey
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
	return &wizard.Result{VM: vm, Password: ctPassword}, nil
}

// volidDisplay returns a friendly template name from a vztmpl volume id.
func volidDisplay(volid string) string {
	if i := strings.LastIndex(volid, "/"); i >= 0 {
		return volid[i+1:]
	}
	return volid
}
