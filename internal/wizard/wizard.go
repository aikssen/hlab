// Package wizard implements the interactive `hlab vm create` flow described in
// docs/wizard.md. It discovers infrastructure from Proxmox and asks only what
// cannot be inferred, grouped into a few thematic screens.
package wizard

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/plans"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/software"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/theme"
)

// defaultPlanName preselects KVM2 if present, else the first plan, else Custom.
func defaultPlanName(ps []plans.Plan) string {
	if _, ok := plans.ByName(ps, "KVM2"); ok {
		return "KVM2"
	}
	if len(ps) > 0 {
		return ps[0].Name
	}
	return plans.Custom
}

// Result is the outcome of the wizard.
type Result struct {
	VM       *state.VMSpec
	Password string // cloud-init password, empty if SSH-only
}

const sshNone = "none"

// Run drives the full VM-creation wizard. pm is used for live discovery.
// suggestedIP, when set, pre-fills the static IP field (CIDR included).
func Run(cfg *config.Config, pm *proxmox.Client, suggestedIP string) (*Result, error) {
	// All forms follow the configured theme, matching the CLI result boxes and the
	// dashboard.
	formTheme := theme.Huh(theme.Get(cfg.Theme))
	// Discover every node's templates up front. The chosen template determines the
	// node the VM is created on: VM ids are cluster-unique and, with node-local
	// storage, a clone must land on the template's node — so there is no separate
	// node prompt.
	nodes, err := pm.Nodes()
	if err != nil {
		return nil, fmt.Errorf("discovering nodes: %w", err)
	}
	var templates []proxmox.Template
	templateByID := make(map[int]proxmox.Template)
	for _, n := range nodes {
		ts, _ := pm.Templates(n.Name)
		for _, t := range ts {
			templates = append(templates, t)
			templateByID[t.VMID] = t
		}
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("no VM templates found in the cluster")
	}
	tmplOpts := make([]huh.Option[int], 0, len(templates))
	for _, t := range templates {
		tmplOpts = append(tmplOpts, huh.NewOption(fmt.Sprintf("%s (#%d) · %s", t.Name, t.VMID, t.Node), t.VMID))
	}
	// Storage is not asked here — it is configured once in `hlab setup` (like the
	// default bridge) and reused for every VM.
	storage := cfg.DefaultStorage

	catalog, _ := plans.Load()
	planName := defaultPlanName(catalog)
	planOpts := make([]huh.Option[string], 0, len(catalog)+1)
	for _, p := range catalog {
		planOpts = append(planOpts, huh.NewOption(p.DisplayLabel(), p.Name))
	}
	planOpts = append(planOpts, huh.NewOption("Custom (enter specs)", plans.Custom))

	// --- Screen 1: Template (image) — chosen first (you decide what to run before
	// what size it needs). It also determines the node. ---
	var templateID int
	// Preselect the configured default template, if present.
	if cfg.DefaultTemplate != "" {
		for _, t := range templates {
			if t.Name == cfg.DefaultTemplate {
				templateID = t.VMID
				break
			}
		}
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().Title("Template").
			Description("The VM is created on the template's node.").
			Options(tmplOpts...).Value(&templateID),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}
	// The chosen template determines the node and the template name.
	tmpl := templateByID[templateID]
	node, templateName := tmpl.Node, tmpl.Name
	// The clone cannot shrink the template's disk, so default to (and enforce) it.
	tmplDiskGB, _ := pm.TemplateDiskGB(node, templateID)
	if tmplDiskGB <= 0 {
		tmplDiskGB = 10
	}

	// --- Screen 2: Plan (size) ---
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Plan").
			Description("Preconfigured size, or Custom to enter your own.").
			Options(planOpts...).Value(&planName),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// --- Screen 3: custom specs — only for a Custom plan, right after the plan. ---
	coresStr, memGBStr, diskGBStr := "2", "4", strconv.Itoa(tmplDiskGB)
	if planName == plans.Custom {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("CPU cores").Value(&coresStr).Validate(validatePositiveInt),
			huh.NewInput().Title("Memory (GB)").Value(&memGBStr).Validate(validatePositiveInt),
			huh.NewInput().Title("Disk (GB)").
				Description(fmt.Sprintf("Template disk is %d GB; cannot be smaller.", tmplDiskGB)).
				Value(&diskGBStr).Validate(func(s string) error {
				n, err := strconv.Atoi(strings.TrimSpace(s))
				if err != nil || n <= 0 {
					return fmt.Errorf("enter a positive number")
				}
				if n < tmplDiskGB {
					return fmt.Errorf("must be >= template size (%d GB)", tmplDiskGB)
				}
				return nil
			}),
		)).WithTheme(formTheme).Run(); err != nil {
			return nil, err
		}
	}
	// A preconfigured plan overrides the (skipped) manual specs.
	planRecord := ""
	if p, ok := plans.ByName(catalog, planName); ok {
		coresStr = strconv.Itoa(p.Cores)
		memGBStr = strconv.Itoa(p.MemoryGB)
		diskGBStr = strconv.Itoa(p.DiskGB)
		planRecord = p.Name
	}

	// --- Screen 4: identity + networking ---
	var (
		vmidStr  string
		hostname string
		netMode  = "dhcp"
		ipCIDR   = suggestedIP
		gateway  = cfg.DefaultGateway
		dnsStr   string
	)
	// Default to static addressing when a gateway is configured, otherwise DHCP.
	if cfg.DefaultGateway != "" {
		netMode = "static"
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("VM ID").Description("Chosen manually (you organize by ranges).").
			Value(&vmidStr).Validate(validatePositiveInt),
		huh.NewInput().Title("Hostname").Description("Becomes the VM name, hostname, terraform key and inventory name.").
			Value(&hostname).Validate(validateHostname),
		huh.NewSelect[string]().Title("Networking").
			Options(huh.NewOption("DHCP", "dhcp"), huh.NewOption("Static", "static")).
			Value(&netMode),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// --- Screen 5: static addressing — only when Static, right after networking. ---
	if netMode == "static" {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("IP address (CIDR)").Placeholder("192.168.1.50/24").
				Value(&ipCIDR).Validate(validateNonEmpty),
			huh.NewInput().Title("Gateway").Placeholder("192.168.1.1").
				Value(&gateway).Validate(validateNonEmpty),
			huh.NewInput().Title("DNS servers (optional, comma-separated)").
				Placeholder("192.168.1.1").Value(&dnsStr),
		)).WithTheme(formTheme).Run(); err != nil {
			return nil, err
		}
	}

	// --- Screen 6: User & login ---
	username := cfg.CreateUserDefault()
	var password string
	sshChoice := cfg.DefaultSSHKey
	sshOpts := []huh.Option[string]{huh.NewOption("none", sshNone)}
	for _, k := range cfg.SSHKeys {
		sshOpts = append(sshOpts, huh.NewOption(k.Name, k.Name))
	}
	if sshChoice == "" && len(cfg.SSHKeys) > 0 {
		sshChoice = cfg.SSHKeys[0].Name
	}
	if sshChoice == "" {
		sshChoice = sshNone
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Administrative username").Value(&username).Validate(validateNonEmpty),
		huh.NewInput().Title("Password").Description("Required — a password guarantees a login method (an SSH key is optional and additive).").
			EchoMode(huh.EchoModePassword).Value(&password).Validate(validateNonEmpty),
		huh.NewSelect[string]().Title("SSH key (optional)").Options(sshOpts...).Value(&sshChoice),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// Software and dotfiles are chosen later, in `hlab vm provision`, where they
	// are actually installed.

	// Assemble the declaration.
	vm := &state.VMSpec{
		Name:        hostname,
		Node:        node,
		VMID:        atoi(vmidStr),
		Template:    templateName,
		TemplateID:  templateID,
		Storage:     storage,
		Bridge:      cfg.DefaultBridge,
		Plan:        planRecord,
		Cores:       atoi(coresStr),
		MemoryGB:    atoi(memGBStr),
		DiskGB:      atoi(diskGBStr),
		DHCP:        netMode == "dhcp",
		Username:    username,
		HasPassword: strings.TrimSpace(password) != "",
	}
	if netMode == "static" {
		vm.IPCIDR = strings.TrimSpace(ipCIDR)
		vm.Gateway = strings.TrimSpace(gateway)
		vm.DNS = splitCSV(dnsStr)
	}
	if sshChoice != sshNone {
		if pub, ok := cfg.SSHKeyByName(sshChoice); ok {
			vm.SSHKeys = []string{pub}
		}
	}

	// --- Screen 7: review & confirm ---
	confirm := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Review").Description(summary(vm, password)),
		huh.NewConfirm().Title("Create this VM?").Value(&confirm),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}
	if !confirm {
		return nil, nil // user cancelled
	}

	return &Result{VM: vm, Password: password}, nil
}

// defaultLXCPlanName preselects micro if present, else the first plan, else Custom.
func defaultLXCPlanName(ps []plans.Plan) string {
	if _, ok := plans.ByName(ps, "micro"); ok {
		return "micro"
	}
	if len(ps) > 0 {
		return ps[0].Name
	}
	return plans.Custom
}

// RunCT drives the interactive LXC container-creation flow. It mirrors Run but
// uses container templates (vztmpl), the LXC plan catalog, container-only options
// (unprivileged/nesting/swap) and a root login. The guest type is already LXC (the
// caller is `hlab ct create`), so there is no VM-vs-LXC screen here.
func RunCT(cfg *config.Config, pm *proxmox.Client, suggestedIP string) (*Result, error) {
	// All forms follow the configured theme, matching the CLI result boxes and the
	// dashboard.
	formTheme := theme.Huh(theme.Get(cfg.Theme))
	// Discover container templates across the cluster. The chosen template
	// determines the node (ids are cluster-unique; with node-local storage the
	// rootfs must land on the template's node).
	templates, err := pm.AllContainerTemplates()
	if err != nil {
		return nil, fmt.Errorf("discovering container templates: %w", err)
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("no container templates (vztmpl) found in the cluster — download one in Proxmox first")
	}
	tmplByVolID := make(map[string]proxmox.ContainerTemplate, len(templates))
	tmplOpts := make([]huh.Option[string], 0, len(templates))
	for _, t := range templates {
		tmplByVolID[t.VolID] = t
		tmplOpts = append(tmplOpts, huh.NewOption(fmt.Sprintf("%s · %s", t.Name, t.Node), t.VolID))
	}

	catalog, _ := plans.LoadLXC()
	planName := defaultLXCPlanName(catalog)
	planOpts := make([]huh.Option[string], 0, len(catalog)+1)
	for _, p := range catalog {
		planOpts = append(planOpts, huh.NewOption(p.DisplayLabel(), p.Name))
	}
	planOpts = append(planOpts, huh.NewOption("Custom (enter specs)", plans.Custom))

	// --- Screen 1: Template (image) ---
	var templateFile string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Container template").
			Description("The container is created on the template's node.").
			Options(tmplOpts...).Value(&templateFile),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}
	tmpl := tmplByVolID[templateFile]
	node := tmpl.Node
	osType := proxmox.OSTypeFromTemplate(templateFile)

	// --- Screen 2: Plan (size) ---
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Plan").
			Description("Preconfigured size, or Custom to enter your own.").
			Options(planOpts...).Value(&planName),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// --- Screen 3: custom specs — only for a Custom plan. Memory is in MB (LXC
	// tiers are commonly sub-GB); there is no template disk floor for containers. ---
	coresStr, memMBStr, diskGBStr := "1", "512M", "4"
	if planName == plans.Custom {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("CPU cores").Value(&coresStr).Validate(validatePositiveInt),
			huh.NewInput().Title("Memory (GB, e.g. 2 or 512M)").Description("512 MB is a safe minimum for a container.").
				Value(&memMBStr).Validate(validateMemMB),
			huh.NewInput().Title("Disk (GB)").Value(&diskGBStr).Validate(validatePositiveInt),
		)).WithTheme(formTheme).Run(); err != nil {
			return nil, err
		}
	}
	planRecord := ""
	memMB, _ := plans.ParseMem(memMBStr) // custom input (GB default, M/MB suffix for sub-GB)
	if p, ok := plans.ByName(catalog, planName); ok {
		coresStr = strconv.Itoa(p.Cores)
		diskGBStr = strconv.Itoa(p.DiskGB)
		planRecord = p.Name
		memMB = p.MB() // a plan's size is authoritative (already in MB)
	}

	// --- Screen 4: container options (unprivileged / swap) — nesting is always
	// on (modern systemd guests misbehave without it, and it lets Docker/Podman run
	// inside), so it is not a choice. ---
	unprivileged := true
	swapStr := "0"
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Unprivileged container?").
			Description("Recommended: maps container root to an unprivileged host user.").
			Value(&unprivileged),
		huh.NewInput().Title("Swap (MB)").Description("0 = none.").
			Value(&swapStr).Validate(validateNonNegativeInt),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// --- Screen 5: identity + networking ---
	var (
		vmidStr  string
		hostname string
		netMode  = "static" // static is recommended for containers (no agent for DHCP discovery)
		ipCIDR   = suggestedIP
		gateway  = cfg.DefaultGateway
		dnsStr   string
	)
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Container ID").Description("Chosen manually (you organize by ranges).").
			Value(&vmidStr).Validate(validatePositiveInt),
		huh.NewInput().Title("Hostname").Description("Becomes the container name, hostname, terraform key and inventory name.").
			Value(&hostname).Validate(validateHostname),
		huh.NewSelect[string]().Title("Networking").
			Description("Static is recommended: containers have no agent, so a DHCP address can't be auto-discovered.").
			Options(huh.NewOption("Static", "static"), huh.NewOption("DHCP", "dhcp")).
			Value(&netMode),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	// --- Screen 6: static addressing — only when Static. ---
	if netMode == "static" {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("IP address (CIDR)").Placeholder("192.168.1.60/24").
				Value(&ipCIDR).Validate(validateNonEmpty),
			huh.NewInput().Title("Gateway").Placeholder("192.168.1.1").
				Value(&gateway).Validate(validateNonEmpty),
			huh.NewInput().Title("DNS servers (optional, comma-separated)").
				Placeholder("192.168.1.1").Value(&dnsStr),
		)).WithTheme(formTheme).Run(); err != nil {
			return nil, err
		}
	}

	// --- Screen 7: root login (containers log in as root) ---
	var password string
	sshChoice := cfg.DefaultSSHKey
	sshOpts := []huh.Option[string]{huh.NewOption("none", sshNone)}
	for _, k := range cfg.SSHKeys {
		sshOpts = append(sshOpts, huh.NewOption(k.Name, k.Name))
	}
	if sshChoice == "" && len(cfg.SSHKeys) > 0 {
		sshChoice = cfg.SSHKeys[0].Name
	}
	if sshChoice == "" {
		sshChoice = sshNone
	}
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Login").Description("Containers log in as root; the key/password below apply to root."),
		huh.NewInput().Title("Root password").Description("Required (Proxmox refuses a passwordless container). An SSH key is optional and additive.").
			EchoMode(huh.EchoModePassword).Value(&password).Validate(validateNonEmpty),
		huh.NewSelect[string]().Title("SSH key (optional)").Options(sshOpts...).Value(&sshChoice),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}

	vm := &state.VMSpec{
		Name:         hostname,
		Type:         "lxc",
		Node:         node,
		VMID:         atoi(vmidStr),
		Template:     tmpl.Name,
		TemplateFile: templateFile,
		OSType:       osType,
		Storage:      cfg.DefaultStorage,
		Bridge:       cfg.DefaultBridge,
		Plan:         planRecord,
		Cores:        atoi(coresStr),
		MemoryMB:     memMB,
		DiskGB:       atoi(diskGBStr),
		SwapMB:       atoi(swapStr),
		Unprivileged: unprivileged,
		Nesting:      true, // always on for hlab-created containers
		DHCP:         netMode == "dhcp",
		Username:     "root",
		HasPassword:  strings.TrimSpace(password) != "",
	}
	if netMode == "static" {
		vm.IPCIDR = strings.TrimSpace(ipCIDR)
		vm.Gateway = strings.TrimSpace(gateway)
		vm.DNS = splitCSV(dnsStr)
	}
	if sshChoice != sshNone {
		if pub, ok := cfg.SSHKeyByName(sshChoice); ok {
			vm.SSHKeys = []string{pub}
		}
	}

	// --- Review & confirm ---
	confirm := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Review").Description(summaryCT(vm, password)),
		huh.NewConfirm().Title("Create this container?").Value(&confirm),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}
	if !confirm {
		return nil, nil
	}
	return &Result{VM: vm, Password: password}, nil
}

func summaryCT(vm *state.VMSpec, password string) string {
	login := "ssh key"
	if password != "" {
		login = "password"
		if len(vm.SSHKeys) > 0 {
			login = "password + ssh key"
		}
	}
	net := "DHCP"
	if !vm.DHCP {
		net = vm.IPCIDR + " gw " + vm.Gateway
	}
	return strings.Join([]string{
		fmt.Sprintf("Node:         %s", vm.Node),
		fmt.Sprintf("Template:     %s", vm.Template),
		fmt.Sprintf("Storage:      %s", vm.Storage),
		fmt.Sprintf("CT ID:        %d", vm.VMID),
		fmt.Sprintf("Name:         %s", vm.Name),
		fmt.Sprintf("CPU:          %d cores", vm.Cores),
		fmt.Sprintf("RAM:          %d MB", vm.MemoryMB),
		fmt.Sprintf("Disk:         %d GB", vm.DiskGB),
		fmt.Sprintf("Unprivileged: %v", vm.Unprivileged),
		"Nesting:      on",
		fmt.Sprintf("Network:      %s", net),
		fmt.Sprintf("Login:        root (%s)", login),
	}, "\n")
}

func summary(vm *state.VMSpec, password string) string {
	login := "ssh key"
	if password != "" {
		login = "password"
		if len(vm.SSHKeys) > 0 {
			login = "password + ssh key"
		}
	}
	net := "DHCP"
	if !vm.DHCP {
		net = vm.IPCIDR + " gw " + vm.Gateway
	}
	return strings.Join([]string{
		fmt.Sprintf("Node:     %s", vm.Node),
		fmt.Sprintf("Template: %s (#%d)", vm.Template, vm.TemplateID),
		fmt.Sprintf("Storage:  %s", vm.Storage),
		fmt.Sprintf("VM ID:    %d", vm.VMID),
		fmt.Sprintf("Name:     %s", vm.Name),
		fmt.Sprintf("CPU:      %d cores", vm.Cores),
		fmt.Sprintf("RAM:      %d GB", vm.MemoryGB),
		fmt.Sprintf("Disk:     %d GB", vm.DiskGB),
		fmt.Sprintf("Network:  %s", net),
		fmt.Sprintf("User:     %s", vm.Username),
		fmt.Sprintf("Login:    %s", login),
	}, "\n")
}

// ProvisionOptions asks which software to install (dotfiles included as an
// ordinary catalog entry when a dotfiles repo is configured). It is the
// provisioning-phase counterpart of the create wizard, reused by `hlab vm
// provision`. The current selection pre-selects the prompt so re-provisioning
// remembers the previous choice.
func ProvisionOptions(formTheme *huh.Theme, cur []string, dotfilesConfigured bool) ([]string, error) {
	cat, err := software.Catalog()
	if err != nil {
		return nil, err
	}
	items := software.Selectable(cat, dotfilesConfigured)
	swOpts := make([]huh.Option[string], 0, len(items))
	for _, it := range items {
		swOpts = append(swOpts, huh.NewOption(it.Label, it.Key))
	}
	selected := append([]string(nil), cur...)

	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Additional software").
			Description("Installed via Ansible. Runtimes use mise.").
			Options(swOpts...).
			Value(&selected),
	)).WithTheme(formTheme).Run(); err != nil {
		return nil, err
	}
	return selected, nil
}

// --- helpers ---

func validateNonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func validatePositiveInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return fmt.Errorf("enter a positive number")
	}
	return nil
}

// validateMemMB validates a memory value in MB with an optional G/M unit suffix
// (e.g. "512", "1024", "2G").
func validateMemMB(s string) error {
	_, err := plans.ParseMem(s)
	return err
}

func validateNonNegativeInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return fmt.Errorf("enter a non-negative number")
	}
	return nil
}

func validateHostname(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("use lowercase letters, digits and hyphens (kebab-case)")
		}
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
