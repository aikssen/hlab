package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/plans"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/software"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/theme"
	"github.com/aikssen/hlab/internal/wizard"
)

// This file builds the huh forms the dashboard embeds (so the create / provision
// / destroy / setup flows run inside the alt-screen instead of taking over the
// terminal). The CLI keeps its own forms in internal/wizard untouched.

const sshNone = "none"

// --- create ---

type createBinding struct {
	form     *huh.Form
	cfg      *config.Config
	pm       *proxmox.Client
	plans    []plans.Plan // VM plans
	lxcPlans []plans.Plan // container plans

	// guestType is chosen first: "vm" (QEMU) or "lxc" (container). It drives which
	// template source, plan catalog and options screens are shown.
	guestType string

	// templateByID maps a template's (cluster-unique) VM id to the template, so the
	// chosen template determines the node it lives on. The Template select lists
	// every node's templates as static options; this avoids huh's OptionsFunc,
	// which forces a fixed height and (in v1.0.0) pins the selected row to the top
	// of the viewport, hiding the other options as you navigate.
	templateByID map[int]proxmox.Template
	// ctTemplateByVolID maps a container template's volume id to its metadata (node
	// + display name), so the chosen container template determines the node.
	ctTemplateByVolID map[string]proxmox.ContainerTemplate
	// usedVMIDs maps an in-use VM/LXC id to a human label, so the VM ID field can
	// reject a clash before Terraform does.
	usedVMIDs map[int]string

	node       string
	storage    string
	templateID int    // VM template
	ctTemplate string // LXC template volume id

	planName                      string // VM plan name, or plans.Custom
	lxcPlanName                   string // LXC plan name, or plans.Custom
	vmidStr, hostname             string
	coresStr, memStr, diskStr     string // VM specs (memory in GB)
	memMBStr, swapStr             string // LXC specs (memory/swap in MB)
	unprivileged                  bool   // LXC option (nesting is always on)
	netMode, ipCIDR, gateway      string
	dnsStr                        string
	username, password, sshChoice string

	confirm bool
}

func newCreateBinding(cfg *config.Config, pm *proxmox.Client, suggestedIP string) (*createBinding, error) {
	nodes, err := pm.Nodes()
	if err != nil {
		return nil, fmt.Errorf("discovering nodes: %w", err)
	}
	// Collect every node's templates once (cheap, off the render path) into a flat,
	// node-labelled list of static options. Templates are listed for all nodes;
	// the chosen one determines the node the VM is created on.
	var allTemplates []proxmox.Template
	templateByID := make(map[int]proxmox.Template)
	for _, n := range nodes {
		ts, _ := pm.Templates(n.Name)
		for _, t := range ts {
			allTemplates = append(allTemplates, t)
			templateByID[t.VMID] = t
		}
	}
	// Container templates (vztmpl) across the cluster, for the LXC branch.
	var allCTTemplates []proxmox.ContainerTemplate
	ctTemplateByVolID := make(map[string]proxmox.ContainerTemplate)
	if cts, cerr := pm.AllContainerTemplates(); cerr == nil {
		for _, t := range cts {
			allCTTemplates = append(allCTTemplates, t)
			ctTemplateByVolID[t.VolID] = t
		}
	}
	// Build the set of in-use VM/LXC ids so the VM ID field can flag a clash early.
	usedVMIDs := make(map[int]string)
	if guests, gerr := pm.ClusterGuests(); gerr == nil {
		for _, g := range guests {
			usedVMIDs[g.VMID] = fmt.Sprintf("%s (%s on %s)", g.Name, g.Type, g.Node)
		}
	}
	catalog, _ := plans.Load()
	lxcCatalog, _ := plans.LoadLXC()
	b := &createBinding{
		cfg: cfg, pm: pm, plans: catalog, lxcPlans: lxcCatalog,
		guestType:    "vm",
		templateByID: templateByID, ctTemplateByVolID: ctTemplateByVolID, usedVMIDs: usedVMIDs,
		node: cfg.DefaultNode, storage: cfg.DefaultStorage,
		planName:    defaultPlan(catalog),
		lxcPlanName: defaultLXCPlan(lxcCatalog),
		coresStr:    "2", memStr: "4", diskStr: "32",
		memMBStr: "512M", swapStr: "0", unprivileged: true,
		netMode: "dhcp", ipCIDR: suggestedIP, gateway: cfg.DefaultGateway,
		username: cfg.CreateUserDefault(), sshChoice: sshNone,
	}
	if len(allCTTemplates) > 0 {
		b.ctTemplate = allCTTemplates[0].VolID
	}
	// Preselect the configured default template (by name), else the first one, and
	// align the node to it.
	for _, t := range allTemplates {
		if t.Name == cfg.DefaultTemplate {
			b.templateID, b.node = t.VMID, t.Node
			break
		}
	}
	if b.templateID == 0 && len(allTemplates) > 0 {
		b.templateID, b.node = allTemplates[0].VMID, allTemplates[0].Node
	}
	if cfg.DefaultGateway != "" {
		b.netMode = "static"
	}

	planOpts := make([]huh.Option[string], 0, len(catalog)+1)
	for _, p := range catalog {
		planOpts = append(planOpts, huh.NewOption(p.DisplayLabel(), p.Name))
	}
	planOpts = append(planOpts, huh.NewOption("Custom (enter specs)", plans.Custom))
	lxcPlanOpts := make([]huh.Option[string], 0, len(lxcCatalog)+1)
	for _, p := range lxcCatalog {
		lxcPlanOpts = append(lxcPlanOpts, huh.NewOption(p.DisplayLabel(), p.Name))
	}
	lxcPlanOpts = append(lxcPlanOpts, huh.NewOption("Custom (enter specs)", plans.Custom))
	switch {
	case cfg.DefaultSSHKey != "":
		b.sshChoice = cfg.DefaultSSHKey
	case len(cfg.SSHKeys) > 0:
		b.sshChoice = cfg.SSHKeys[0].Name
	}

	tmplOpts := make([]huh.Option[int], 0, len(allTemplates))
	for _, t := range allTemplates {
		tmplOpts = append(tmplOpts, huh.NewOption(fmt.Sprintf("%s (#%d) · %s", t.Name, t.VMID, t.Node), t.VMID))
	}
	ctTmplOpts := make([]huh.Option[string], 0, len(allCTTemplates))
	for _, t := range allCTTemplates {
		ctTmplOpts = append(ctTmplOpts, huh.NewOption(fmt.Sprintf("%s · %s", t.Name, t.Node), t.VolID))
	}
	sshOpts := []huh.Option[string]{huh.NewOption("none", sshNone)}
	for _, k := range cfg.SSHKeys {
		sshOpts = append(sshOpts, huh.NewOption(k.Name, k.Name))
	}

	isVM := func() bool { return b.guestType != "lxc" }
	isLXC := func() bool { return b.guestType == "lxc" }

	b.form = huh.NewForm(
		// Screen 0: guest type — VM or LXC container. Drives the rest of the flow.
		huh.NewGroup(
			huh.NewSelect[string]().Title("Guest type").
				Description("A full VM, or a lighter LXC container (shared kernel).").
				Options(huh.NewOption("Virtual machine (VM)", "vm"), huh.NewOption("LXC container", "lxc")).
				Value(&b.guestType),
		),
		// Screen 1 (VM): pick the image (template) first. The template also
		// determines the node.
		huh.NewGroup(
			huh.NewSelect[int]().Title("Template").
				Description("The VM is created on the template's node.").
				Options(tmplOpts...).Value(&b.templateID),
		).WithHideFunc(isLXC),
		// Screen 1 (LXC): pick the container template (vztmpl).
		huh.NewGroup(
			huh.NewSelect[string]().Title("Container template").
				Description("The container is created on the template's node.").
				Options(ctTmplOpts...).Value(&b.ctTemplate),
		).WithHideFunc(isVM),
		// Screen 2 (VM): pick a size (plan).
		huh.NewGroup(
			huh.NewSelect[string]().Title("Plan").
				Description("Preconfigured size, or Custom to enter your own.").
				Options(planOpts...).Value(&b.planName),
		).WithHideFunc(isLXC),
		// Screen 2 (LXC): pick a container size (plan).
		huh.NewGroup(
			huh.NewSelect[string]().Title("Plan").
				Description("Preconfigured size, or Custom to enter your own.").
				Options(lxcPlanOpts...).Value(&b.lxcPlanName),
		).WithHideFunc(isVM),
		// Screen 3 (VM): custom specs — shown only for a Custom VM plan.
		huh.NewGroup(
			huh.NewInput().Title("CPU cores").Value(&b.coresStr).Validate(validatePosInt),
			huh.NewInput().Title("Memory (GB)").Value(&b.memStr).Validate(validatePosInt),
			huh.NewInput().Title("Disk (GB)").
				Description("Bumped up to the template size if smaller.").
				Value(&b.diskStr).Validate(validatePosInt),
		).WithHideFunc(func() bool { return isLXC() || b.planName != plans.Custom }),
		// Screen 3 (LXC): custom specs — memory in MB, no template disk floor.
		huh.NewGroup(
			huh.NewInput().Title("CPU cores").Value(&b.coresStr).Validate(validatePosInt),
			huh.NewInput().Title("Memory (GB, e.g. 2 or 512M)").Description("512 MB is a safe minimum.").
				Value(&b.memMBStr).Validate(validateMemMB),
			huh.NewInput().Title("Disk (GB)").Value(&b.diskStr).Validate(validatePosInt),
		).WithHideFunc(func() bool { return isVM() || b.lxcPlanName != plans.Custom }),
		// Screen 4 (LXC): container options. Nesting is always on (modern systemd
		// guests misbehave without it, and it lets Docker/Podman run inside), so it
		// is not a choice.
		huh.NewGroup(
			huh.NewConfirm().Title("Unprivileged container?").
				Description("Recommended: maps container root to an unprivileged host user.").
				Value(&b.unprivileged),
			huh.NewInput().Title("Swap (MB)").Description("0 = none.").
				Value(&b.swapStr).Validate(validateNonNegInt),
		).WithHideFunc(isVM),
		// Screen 5: identity + networking.
		huh.NewGroup(
			huh.NewInput().TitleFunc(func() string {
				if isLXC() {
					return "Container ID"
				}
				return "VM ID"
			}, &b.guestType).Value(&b.vmidStr).Validate(b.validateVMID),
			huh.NewInput().Title("Hostname").Value(&b.hostname).Validate(validateHostname),
			huh.NewSelect[string]().Title("Networking").
				Options(huh.NewOption("DHCP", "dhcp"), huh.NewOption("Static", "static")).
				Value(&b.netMode),
		),
		// Screen 6: static addressing — shown only for Static.
		huh.NewGroup(
			huh.NewInput().Title("IP address (CIDR)").Placeholder("192.168.1.50/24").Value(&b.ipCIDR),
			huh.NewInput().Title("Gateway").Placeholder("192.168.1.1").Value(&b.gateway),
			huh.NewInput().Title("DNS servers (optional, comma-separated)").Value(&b.dnsStr),
		).WithHideFunc(func() bool { return b.netMode != "static" }),
		// Screen 7 (VM): user & login. A password is required (it guarantees a login
		// method); an SSH key is optional and additive.
		huh.NewGroup(
			huh.NewInput().Title("Administrative username").Value(&b.username).Validate(validateNonEmpty),
			huh.NewInput().Title("Password").Description("Required — a password guarantees a login method (an SSH key is optional and additive).").
				EchoMode(huh.EchoModePassword).Value(&b.password).Validate(validateNonEmpty),
			huh.NewSelect[string]().Title("SSH key (optional)").Options(sshOpts...).Value(&b.sshChoice),
		).WithHideFunc(isLXC),
		// Screen 7 (LXC): root login (containers log in as root). A password is
		// required (Proxmox refuses a passwordless container); an SSH key is optional.
		huh.NewGroup(
			huh.NewNote().Title("Login").Description("Containers log in as root; the key/password apply to root."),
			huh.NewInput().Title("Root password").Description("Required (Proxmox refuses a passwordless container). An SSH key is optional and additive.").
				EchoMode(huh.EchoModePassword).Value(&b.password).Validate(validateNonEmpty),
			huh.NewSelect[string]().Title("SSH key (optional)").Options(sshOpts...).Value(&b.sshChoice),
		).WithHideFunc(isVM),
		huh.NewGroup(
			huh.NewNote().TitleFunc(func() string { return "Review" }, &b.guestType).
				DescriptionFunc(b.summary, []any{
					&b.guestType, &b.node, &b.storage, &b.templateID, &b.ctTemplate,
					&b.vmidStr, &b.hostname, &b.planName, &b.lxcPlanName,
					&b.coresStr, &b.memStr, &b.memMBStr, &b.diskStr, &b.swapStr,
					&b.unprivileged, &b.netMode, &b.ipCIDR,
					&b.gateway, &b.username, &b.password, &b.sshChoice,
				}),
			huh.NewConfirm().TitleFunc(func() string {
				if isLXC() {
					return "Create this container?"
				}
				return "Create this VM?"
			}, &b.guestType).Value(&b.confirm),
		),
	)
	return b, nil
}

// validateVMID enforces a positive integer that isn't already taken by another
// guest in the cluster, so a clash is caught in the form instead of mid-apply.
func (b *createBinding) validateVMID(s string) error {
	if err := validatePosInt(s); err != nil {
		return err
	}
	if who, used := b.usedVMIDs[atoiSafe(s)]; used {
		return fmt.Errorf("already in use by %s", who)
	}
	return nil
}

// summary renders the review text shown before confirming creation.
func (b *createBinding) summary() string {
	net := "DHCP"
	if b.netMode == "static" {
		net = strings.TrimSpace(b.ipCIDR) + " gw " + strings.TrimSpace(b.gateway)
	}
	login := "ssh key"
	if strings.TrimSpace(b.password) != "" {
		login = "password"
		if b.sshChoice != sshNone {
			login = "password + ssh key"
		}
	}
	if b.guestType == "lxc" {
		cores, mem, disk, planLine := b.coresStr, strconv.Itoa(parseMBSafe(b.memMBStr)), b.diskStr, "Custom"
		if p, ok := plans.ByName(b.lxcPlans, b.lxcPlanName); ok {
			cores, mem, disk = strconv.Itoa(p.Cores), strconv.Itoa(p.MB()), strconv.Itoa(p.DiskGB)
			planLine = p.Name
		}
		node, tmpl := b.node, b.ctTemplate
		if t, ok := b.ctTemplateByVolID[b.ctTemplate]; ok {
			node, tmpl = t.Node, t.Name
		}
		return strings.Join([]string{
			"Type:         LXC container",
			"Node:         " + node,
			"Template:     " + tmpl,
			"CT ID:        " + b.vmidStr,
			"Name:         " + b.hostname,
			"Plan:         " + planLine,
			"CPU/RAM:      " + cores + " cores / " + mem + " MB",
			"Disk:         " + disk + " GB",
			"Unprivileged: " + yesNoBool(b.unprivileged),
			"Nesting:      on",
			"Network:      " + net,
			"Login:        root (" + login + ")",
		}, "\n")
	}
	cores, mem, disk, planLine := b.coresStr, b.memStr, b.diskStr, "Custom"
	if p, ok := plans.ByName(b.plans, b.planName); ok {
		cores, mem, disk = strconv.Itoa(p.Cores), strconv.Itoa(p.MemoryGB), strconv.Itoa(p.DiskGB)
		planLine = p.Name
	}
	node := b.node
	if t, ok := b.templateByID[b.templateID]; ok && t.Node != "" {
		node = t.Node
	}
	return strings.Join([]string{
		"Type:    Virtual machine",
		"Node:    " + node,
		"Storage: " + b.storage,
		"VM ID:   " + b.vmidStr,
		"Name:    " + b.hostname,
		"Plan:    " + planLine,
		"CPU/RAM: " + cores + " cores / " + mem + " GB",
		"Disk:    " + disk + " GB",
		"Network: " + net,
		"User:    " + b.username,
		"Login:   " + login,
	}, "\n")
}

func yesNoBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// defaultPlan returns the preselected VM plan: KVM2 if present, else the first
// plan, else Custom.
func defaultPlan(ps []plans.Plan) string {
	if _, ok := plans.ByName(ps, "KVM2"); ok {
		return "KVM2"
	}
	if len(ps) > 0 {
		return ps[0].Name
	}
	return plans.Custom
}

// defaultLXCPlan returns the preselected LXC plan: micro if present, else the
// first plan, else Custom.
func defaultLXCPlan(ps []plans.Plan) string {
	if _, ok := plans.ByName(ps, "micro"); ok {
		return "micro"
	}
	if len(ps) > 0 {
		return ps[0].Name
	}
	return plans.Custom
}

// Result builds the guest declaration (VM or container) from the completed form.
func (b *createBinding) Result() (*wizard.Result, error) {
	if b.guestType == "lxc" {
		return b.resultLXC()
	}
	// The chosen template determines its node (VM ids are cluster-unique).
	tmpl := b.templateByID[b.templateID]
	if tmpl.Node != "" {
		b.node = tmpl.Node
	}
	vm := &state.VMSpec{
		Name:        strings.TrimSpace(b.hostname),
		Node:        b.node,
		VMID:        atoiSafe(b.vmidStr),
		Template:    tmpl.Name,
		TemplateID:  b.templateID,
		Storage:     b.storage,
		Bridge:      b.cfg.DefaultBridge,
		Cores:       atoiSafe(b.coresStr),
		MemoryGB:    atoiSafe(b.memStr),
		DiskGB:      atoiSafe(b.diskStr),
		DHCP:        b.netMode == "dhcp",
		Username:    strings.TrimSpace(b.username),
		HasPassword: strings.TrimSpace(b.password) != "",
	}
	// A preconfigured plan overrides the (hidden) manual specs.
	if p, ok := plans.ByName(b.plans, b.planName); ok {
		vm.Plan = p.Name
		vm.Cores, vm.MemoryGB, vm.DiskGB = p.Cores, p.MemoryGB, p.DiskGB
	}
	// The clone cannot shrink the template's disk; bump up if needed.
	if tg, _ := b.pm.TemplateDiskGB(b.node, b.templateID); tg > 0 && vm.DiskGB < tg {
		vm.DiskGB = tg
	}
	if !vm.DHCP {
		vm.IPCIDR = strings.TrimSpace(b.ipCIDR)
		vm.Gateway = strings.TrimSpace(b.gateway)
		vm.DNS = splitCSV(b.dnsStr)
	}
	if b.sshChoice != sshNone {
		if pub, ok := b.cfg.SSHKeyByName(b.sshChoice); ok {
			vm.SSHKeys = []string{pub}
		}
	}
	if vm.Name == "" {
		return nil, fmt.Errorf("hostname is required")
	}
	if vm.VMID == 0 {
		return nil, fmt.Errorf("vm id is required")
	}
	return &wizard.Result{VM: vm, Password: b.password}, nil
}

// resultLXC builds a container declaration from the completed form.
func (b *createBinding) resultLXC() (*wizard.Result, error) {
	tmpl := b.ctTemplateByVolID[b.ctTemplate]
	node := b.node
	if tmpl.Node != "" {
		node = tmpl.Node
	}
	vm := &state.VMSpec{
		Name:         strings.TrimSpace(b.hostname),
		Type:         "lxc",
		Node:         node,
		VMID:         atoiSafe(b.vmidStr),
		Template:     tmpl.Name,
		TemplateFile: b.ctTemplate,
		OSType:       proxmox.OSTypeFromTemplate(b.ctTemplate),
		Storage:      b.storage,
		Bridge:       b.cfg.DefaultBridge,
		Cores:        atoiSafe(b.coresStr),
		MemoryMB:     parseMBSafe(b.memMBStr),
		DiskGB:       atoiSafe(b.diskStr),
		SwapMB:       atoiSafe(b.swapStr),
		Unprivileged: b.unprivileged,
		Nesting:      true, // always on for hlab-created containers
		DHCP:         b.netMode == "dhcp",
		Username:     "root",
		HasPassword:  strings.TrimSpace(b.password) != "",
	}
	if p, ok := plans.ByName(b.lxcPlans, b.lxcPlanName); ok {
		vm.Plan = p.Name
		vm.Cores, vm.MemoryMB, vm.DiskGB = p.Cores, p.MB(), p.DiskGB
	}
	if !vm.DHCP {
		vm.IPCIDR = strings.TrimSpace(b.ipCIDR)
		vm.Gateway = strings.TrimSpace(b.gateway)
		vm.DNS = splitCSV(b.dnsStr)
	}
	if b.sshChoice != sshNone {
		if pub, ok := b.cfg.SSHKeyByName(b.sshChoice); ok {
			vm.SSHKeys = []string{pub}
		}
	}
	if vm.Name == "" {
		return nil, fmt.Errorf("hostname is required")
	}
	if vm.VMID == 0 {
		return nil, fmt.Errorf("container id is required")
	}
	if vm.TemplateFile == "" {
		return nil, fmt.Errorf("a container template is required")
	}
	return &wizard.Result{VM: vm, Password: b.password}, nil
}

// --- provision ---

type provBinding struct {
	form     *huh.Form
	software []string
}

func newProvBinding(vm *state.VMSpec, dotfilesConfigured bool) (*provBinding, error) {
	cat, err := software.Catalog()
	if err != nil {
		return nil, err
	}
	items := software.Selectable(cat, dotfilesConfigured)
	b := &provBinding{software: append([]string(nil), vm.Software...)}
	swOpts := make([]huh.Option[string], 0, len(items))
	for _, it := range items {
		swOpts = append(swOpts, huh.NewOption(it.Label, it.Key))
	}
	b.form = huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Additional software").
			Description("Installed via Ansible. Runtimes use mise.").
			Options(swOpts...).
			Value(&b.software),
	))
	return b, nil
}

// --- destroy ---

type destroyBinding struct {
	form    *huh.Form
	confirm bool
}

func newDestroyBinding(name string) *destroyBinding {
	b := &destroyBinding{}
	b.form = huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Destroy VM %q? This deletes it in Proxmox.", name)).
			Value(&b.confirm),
	))
	return b
}

// --- inject ssh key ---

type injectBinding struct {
	form     *huh.Form
	keys     []config.SSHKey // deduped operator keys (config + ~/.ssh)
	pub      string          // selected public key contents
	password string          // console root password (only when needPassword)
	confirm  bool
	needPass bool // true => the guest is a keyless LXC with no stored password
}

// newInjectBinding builds the SSH-key picker for the dashboard's inject action:
// the operator's configured keys (cfg.SSHKeys) plus the keys scanned from ~/.ssh
// (config.ScanSSHKeys), deduped by contents and labelled "name (path)" like the
// CLI/setup pickers. cfg.DefaultSSHKey is preselected when set. guestName names
// the target in the picker title. When needPassword is set (a keyless LXC whose
// root password is not stored on this machine), it also asks for the root password
// hlab needs to log in to the Proxmox console and seed the first key. Returns an
// error when no keys are available.
func newInjectBinding(cfg *config.Config, guestName string, needPassword bool) (*injectBinding, error) {
	keys := operatorSSHKeys(cfg)
	if len(keys) == 0 {
		return nil, fmt.Errorf("no SSH public keys found in config or ~/.ssh")
	}
	b := &injectBinding{keys: keys, needPass: needPassword}
	if pub, ok := cfg.SSHKeyByName(cfg.DefaultSSHKey); ok {
		b.pub = pub
	} else {
		b.pub = keys[0].Pub
	}
	opts := make([]huh.Option[string], 0, len(keys))
	for _, k := range keys {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", k.Name, k.Path), k.Pub))
	}
	fields := []huh.Field{
		huh.NewSelect[string]().
			Title(fmt.Sprintf("SSH key to add to %s", guestName)).
			Description("Installed on the live guest and recorded in its declaration.").
			Options(opts...).Value(&b.pub),
	}
	if needPassword {
		fields = append(fields, huh.NewInput().
			Title(fmt.Sprintf("Root password for %s (Proxmox console login)", guestName)).
			Description("This container has no SSH key hlab can use and no stored password, so hlab logs into the Proxmox console as root to install the first key.").
			EchoMode(huh.EchoModePassword).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("enter the root password (needed to log in to the Proxmox console)")
				}
				return nil
			}).
			Value(&b.password))
	}
	fields = append(fields, huh.NewConfirm().Title("Add this key?").Value(&b.confirm))
	b.form = huh.NewForm(huh.NewGroup(fields...))
	return b, nil
}

// operatorSSHKeys returns the operator's available public keys: the configured
// ones first, then any additional keys scanned from ~/.ssh, deduped by contents.
func operatorSSHKeys(cfg *config.Config) []config.SSHKey {
	var keys []config.SSHKey
	seen := map[string]bool{}
	add := func(k config.SSHKey) {
		pub := strings.TrimSpace(k.Pub)
		if pub == "" || seen[pub] {
			return
		}
		seen[pub] = true
		keys = append(keys, k)
	}
	for _, k := range cfg.SSHKeys {
		add(k)
	}
	if scanned, err := config.ScanSSHKeys(); err == nil {
		for _, k := range scanned {
			add(k)
		}
	}
	return keys
}

// --- theme ---

type themeBinding struct {
	form   *huh.Form
	set    *theme.Set // merged built-ins + themes.yaml, re-read each time the form opens
	choice string     // selected theme name
}

// newThemeBinding builds the theme selector over the merged theme set. It calls
// theme.Load() (re-reading themes.yaml, seeding it if absent) so edits to the file
// — custom colors or new themes — show up without restarting hlab. current
// pre-selects the active theme; an unknown/empty current defaults to "default".
func newThemeBinding(current string) *themeBinding {
	set, _ := theme.Load() // never nil: falls back to the built-ins on any error
	b := &themeBinding{set: set, choice: strings.ToLower(strings.TrimSpace(current))}
	if !set.Has(b.choice) {
		b.choice = "default"
	}
	names := set.Names()
	opts := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		opts = append(opts, huh.NewOption(n, n))
	}
	b.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Theme").
			Description("Applies immediately and is saved to config.").
			Options(opts...).
			Value(&b.choice),
	))
	return b
}

// --- migrate ---

type migrateBinding struct {
	form    *huh.Form
	toNode  string
	confirm bool
}

// newMigrateBinding builds the form to pick a target node for a VM migration. It
// lists every cluster node except the one the VM is already on.
func newMigrateBinding(vm *state.VMSpec, pm *proxmox.Client) (*migrateBinding, error) {
	nodes, err := pm.Nodes()
	if err != nil {
		return nil, fmt.Errorf("discovering nodes: %w", err)
	}
	opts := make([]huh.Option[string], 0, len(nodes))
	for _, n := range nodes {
		if n.Name == vm.Node {
			continue // can't migrate to the node it is already on
		}
		opts = append(opts, huh.NewOption(n.Name, n.Name))
	}
	if len(opts) == 0 {
		return nil, fmt.Errorf("no other node to migrate %q to", vm.Name)
	}
	b := &migrateBinding{toNode: opts[0].Value}
	b.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("Migrate %s (on %s) to…", vm.Name, vm.Node)).
			Description("Moves the VM to another node, keeping its disk and VM id.").
			Options(opts...).Value(&b.toNode),
		huh.NewConfirm().Title("Migrate this VM?").Value(&b.confirm),
	))
	return b, nil
}

// --- edit / resize ---

type editBinding struct {
	form                      *huh.Form
	plans                     []plans.Plan
	lxc                       bool // container: memory is MB, uses the LXC plan catalog
	planName                  string
	coresStr, memStr, diskStr string
	swapStr                   string // LXC only: swap in MB (independent of the plan)
	minDisk                   int    // the current disk size; the disk can only grow
	confirm                   bool
}

// newEditBinding builds the reconfigure form, pre-filled from the guest's current
// hardware. It mirrors the create form's plan + cores/memory/disk fields; the disk
// field can only grow. For containers, memory is in MB and the LXC plan catalog is
// used.
func newEditBinding(vm *state.VMSpec) *editBinding {
	lxc := vm.IsLXC()
	var catalog []plans.Plan
	memStr, memTitle := "", "Memory (GB)"
	if lxc {
		catalog, _ = plans.LoadLXC()
		memStr, memTitle = plans.FormatMem(vm.MemoryMB), "Memory (GB, e.g. 2 or 512M)"
	} else {
		catalog, _ = plans.Load()
		// FormatMem round-trips the exact size: a whole-GB VM shows "4", an adopted
		// VM with a non-GB size shows e.g. "2560M" instead of being silently
		// truncated to "2" (→ 2048 MB) when applied unchanged. declaredMemMB
		// prefers MemoryMB when set.
		memStr, memTitle = plans.FormatMem(declaredMemMB(vm)), "Memory (GB, e.g. 4 or 2560M)"
	}
	b := &editBinding{
		plans:    catalog,
		lxc:      lxc,
		planName: vm.Plan,
		coresStr: strconv.Itoa(vm.Cores),
		memStr:   memStr,
		diskStr:  strconv.Itoa(vm.DiskGB),
		swapStr:  strconv.Itoa(vm.SwapMB), // LXC only; ignored for VMs
		minDisk:  vm.DiskGB,
	}
	if b.planName == "" {
		b.planName = plans.Custom
	}
	planOpts := make([]huh.Option[string], 0, len(catalog)+1)
	for _, p := range catalog {
		planOpts = append(planOpts, huh.NewOption(p.DisplayLabel(), p.Name))
	}
	planOpts = append(planOpts, huh.NewOption("Custom (enter specs)", plans.Custom))

	b.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Plan").
				Description("Preconfigured size, or Custom to enter your own.").
				Options(planOpts...).Value(&b.planName),
		),
		huh.NewGroup(
			huh.NewInput().Title("CPU cores").Value(&b.coresStr).Validate(validatePosInt),
			huh.NewInput().Title(memTitle).Value(&b.memStr).Validate(b.validateMem),
			huh.NewInput().Title("Disk (GB)").Description("Can only grow.").
				Value(&b.diskStr).Validate(b.validateDiskGrow),
		).WithHideFunc(func() bool { return b.planName != plans.Custom }),
		// Swap is an LXC-only knob and isn't part of any plan, so it stays editable
		// whether a named plan or Custom is selected (it flows right after memory).
		huh.NewGroup(
			huh.NewInput().Title("Swap (MB)").Description("0 = none. Container swap; not set by any plan.").
				Value(&b.swapStr).Validate(validateNonNegInt),
		).WithHideFunc(func() bool { return !b.lxc }),
		huh.NewGroup(
			huh.NewConfirm().Title("Apply these changes?").Value(&b.confirm),
		),
	)
	return b
}

// validateMem validates the memory field via ParseMem: a bare number is GB, an
// M/MB suffix is MB (so "4", "512M" and "2560M" all work), for both VMs and
// containers — the VM path accepts the suffix so a non-GB adopted size survives.
func (b *editBinding) validateMem(s string) error {
	_, err := plans.ParseMem(s)
	return err
}

// validateDiskGrow rejects a positive disk size below the current one (no shrink).
func (b *editBinding) validateDiskGrow(s string) error {
	if err := validatePosInt(s); err != nil {
		return err
	}
	if atoiSafe(s) < b.minDisk {
		return fmt.Errorf("disk can only grow (≥ %d GB)", b.minDisk)
	}
	return nil
}

// apply writes the edited hardware onto vm. A named plan sets all three; Custom
// takes the typed values. Memory is MB for containers, GB for VMs. The disk never
// shrinks below the current size.
func (b *editBinding) apply(vm *state.VMSpec) {
	if p, ok := plans.ByName(b.plans, b.planName); ok {
		vm.Plan, vm.Cores, vm.DiskGB = p.Name, p.Cores, p.DiskGB
		if b.lxc {
			vm.MemoryMB = p.MB()
		} else {
			// Clear MemoryMB (set on adopted VMs with a non-GB size, or a container
			// mistakenly loaded here) so writeTfvars doesn't keep using the stale MB
			// value instead of the newly-declared MemoryGB.
			vm.MemoryGB, vm.MemoryMB = p.MemoryGB, 0
		}
	} else {
		vm.Plan = ""
		vm.Cores, vm.DiskGB = atoiSafe(b.coresStr), atoiSafe(b.diskStr)
		if b.lxc {
			vm.MemoryMB, _ = plans.ParseMem(b.memStr)
		} else {
			// Store whole-GB sizes as MemoryGB (MemoryMB cleared) and a non-GB size
			// as MemoryMB (MemoryGB cleared), matching writeTfvars' preference and
			// how adopt/resize persist an odd size — so re-applying an unchanged
			// 2560 MB VM doesn't narrow it to 2048.
			mb, _ := plans.ParseMem(b.memStr)
			if mb%1024 == 0 {
				vm.MemoryGB, vm.MemoryMB = mb/1024, 0
			} else {
				vm.MemoryGB, vm.MemoryMB = 0, mb
			}
		}
	}
	if vm.DiskGB < b.minDisk {
		vm.DiskGB = b.minDisk // a plan smaller than the current disk still can't shrink it
	}
	// Swap is LXC-only and plan-independent, so it's taken from the field regardless
	// of whether a named plan or Custom was chosen.
	if b.lxc {
		vm.SwapMB = atoiSafe(b.swapStr)
	}
}

// --- adopt ---

// adoptNameRe mirrors the engine's kebab-case rule for a declaration name
// (internal/engine/adopt.go's unexported kebabRe) — duplicated here since the
// form needs to re-validate a name the operator may have edited.
var adoptNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// adoptBinding builds the one-screen confirmation form for adopting a
// discovered guest: the synthesized spec (from Engine.BuildAdoptSpec) is shown
// read-only except for the declaration name and — VMs only — the SSH username,
// both editable and re-validated here.
type adoptBinding struct {
	form     *huh.Form
	spec     *state.VMSpec
	warnings []string
	lxc      bool

	name     string
	username string
	confirm  bool
}

// newAdoptBinding synthesizes the declaration for guest g via
// Engine.BuildAdoptSpec (a couple of quick config reads, done synchronously —
// the same class of call as pm.Nodes() in the migrate form) and builds the
// confirmation form around it.
func newAdoptBinding(eng *engine.Engine, g proxmox.Guest) (*adoptBinding, error) {
	spec, warnings, err := eng.BuildAdoptSpec(g, engine.AdoptOptions{})
	if err != nil {
		return nil, err
	}
	b := &adoptBinding{
		spec:     spec,
		warnings: warnings,
		lxc:      spec.IsLXC(),
		name:     spec.Name,
		username: spec.Username,
	}

	fields := []huh.Field{
		huh.NewInput().Title("Declaration name").
			Description("Lowercase, kebab-case; must be unique.").
			Value(&b.name).
			Validate(func(s string) error { return b.validateName(eng, s) }),
	}
	if !b.lxc {
		fields = append(fields, huh.NewInput().Title("SSH username").
			Value(&b.username).Validate(validateNonEmpty))
	}
	fields = append(fields,
		huh.NewNote().Title("Adopting").Description(b.summary()),
		huh.NewConfirm().Title("Adopt this guest? (hlab never modifies it during adoption)").Value(&b.confirm),
	)
	b.form = huh.NewForm(huh.NewGroup(fields...))
	return b, nil
}

// validateName enforces the same kebab-case rule as the engine and rejects a
// collision with an existing declaration (the engine already checked the
// original name in BuildAdoptSpec; the operator may edit it here).
func (b *adoptBinding) validateName(eng *engine.Engine, s string) error {
	s = strings.TrimSpace(s)
	if !adoptNameRe.MatchString(s) {
		return fmt.Errorf("lowercase, kebab-case, starting with a letter")
	}
	if _, err := eng.Store.Load(s); err == nil {
		return fmt.Errorf("a declaration named %q already exists", s)
	}
	return nil
}

// summary renders the read-only review text: what was discovered on the live
// guest, plus any warnings about what the first managed apply will change.
func (b *adoptBinding) summary() string {
	s := b.spec
	kind := "vm"
	if b.lxc {
		kind = "lxc"
	}
	lines := []string{
		"Node:    " + s.Node,
		fmt.Sprintf("VMID:    %d", s.VMID),
		"Type:    " + kind,
		fmt.Sprintf("CPU/RAM: %d cores / %s", s.Cores, memShort(s)),
		fmt.Sprintf("Disk:    %d GB", s.DiskGB),
		"Storage: " + s.Storage,
		"Bridge:  " + s.Bridge,
	}
	for _, w := range b.warnings {
		lines = append(lines, "! "+w)
	}
	return strings.Join(lines, "\n")
}

// apply writes the (possibly edited) name and username back onto the spec,
// ready for Engine.Adopt.
func (b *adoptBinding) apply() *state.VMSpec {
	b.spec.Name = strings.TrimSpace(b.name)
	if !b.lxc {
		b.spec.Username = strings.TrimSpace(b.username)
	}
	return b.spec
}

// --- snapshot (create) ---

type snapBinding struct {
	form        *huh.Form
	name        string
	description string
	withRAM     bool
}

// newSnapBinding builds the create-snapshot form. The RAM option is only offered
// when allowRAM is set (a running VM — capturing memory state requires it to be
// on, and containers have no live-memory state).
func newSnapBinding(allowRAM bool) *snapBinding {
	b := &snapBinding{}
	fields := []huh.Field{
		huh.NewInput().Title("Snapshot name").Placeholder("before-upgrade").
			Value(&b.name).Validate(validateSnapName),
		huh.NewInput().Title("Description (optional)").Value(&b.description),
	}
	if allowRAM {
		fields = append(fields, huh.NewConfirm().
			Title("Include RAM (live state)?").
			Description("Captures running memory so rollback restores the exact live state.").
			Value(&b.withRAM))
	}
	b.form = huh.NewForm(huh.NewGroup(fields...))
	return b
}

// validateSnapName enforces Proxmox's snapshot naming (start with a letter, then
// letters/digits/_/-, at least two characters).
func validateSnapName(s string) error {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return fmt.Errorf("at least 2 characters")
	}
	if !isASCIILetter(rune(s[0])) {
		return fmt.Errorf("must start with a letter")
	}
	for _, r := range s {
		if !isASCIILetter(r) && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return fmt.Errorf("use letters, digits, _ or -")
		}
	}
	return nil
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// --- setup (edit configuration) ---

type setupBinding struct {
	form *huh.Form
	cfg  *config.Config

	cidrStr  string
	sshNames []string
	scanned  []config.SSHKey
}

// newSetupBinding builds a configuration form pre-filled from the current config.
// It discovers options using the existing credentials; to point hlab at a
// different Proxmox server, run `hlab setup` from the CLI (full re-discovery).
func newSetupBinding(cfg *config.Config, pm *proxmox.Client) (*setupBinding, error) {
	b := &setupBinding{cfg: cfg}
	if cfg.DefaultCIDR > 0 {
		b.cidrStr = strconv.Itoa(cfg.DefaultCIDR)
	}

	storages, _ := pm.Storages(cfg.DefaultNode)
	bridges, _ := pm.Bridges(cfg.DefaultNode)
	templates, _ := pm.AllTemplates()
	nodes, _ := pm.Nodes()

	nodeOpts := stringOpts(nodeNames(nodes))
	storageOpts := storageNameOpts(storages)
	bridgeOpts := stringOpts(bridges)

	b.scanned, _ = config.ScanSSHKeys()
	sshOpts := make([]huh.Option[string], 0, len(b.scanned))
	for _, k := range b.scanned {
		sshOpts = append(sshOpts, huh.NewOption(k.Name, k.Name))
	}
	for _, k := range cfg.SSHKeys {
		b.sshNames = append(b.sshNames, k.Name)
	}

	fields := []huh.Field{
		huh.NewInput().Title("Proxmox URL").Value(&cfg.ProxmoxURL),
		huh.NewInput().Title("API Token ID").Value(&cfg.TokenID),
		huh.NewInput().Title("API Token Secret").EchoMode(huh.EchoModePassword).Value(&cfg.TokenSecret),
		huh.NewConfirm().Title("Skip TLS verification? (self-signed certs)").Value(&cfg.Insecure),
		huh.NewSelect[string]().Title("Discovery node").
			Description("Queried to list storages/bridges (not where VMs run).").
			Options(nodeOpts...).Value(&cfg.DefaultNode),
		huh.NewSelect[string]().Title("Default storage").Options(storageOpts...).Value(&cfg.DefaultStorage),
		huh.NewSelect[string]().Title("Default network bridge").Options(bridgeOpts...).Value(&cfg.DefaultBridge),
	}
	if len(templates) > 0 {
		fields = append(fields, huh.NewSelect[string]().Title("Default template").
			Options(stringOpts(templateNames(templates))...).Value(&cfg.DefaultTemplate))
	}
	// Which models are offered depends on the host's vendor — an Intel model can't
	// start on an AMD host. Best-effort: an unreadable vendor just leaves the
	// vendor-neutral choices.
	if cfg.CPUType == "" {
		cfg.CPUType = config.DefaultCPUType
	}
	fields = append(fields,
		huh.NewSelect[string]().Title("VM CPU model").
			Description("Portable models migrate anywhere but lack PCLMULQDQ, which some binaries need.").
			Options(stringOpts(config.CPUTypeChoices(pm.NodeCPUVendor(cfg.DefaultNode)))...).
			Value(&cfg.CPUType),
		huh.NewInput().Title("Default gateway (optional)").Placeholder("192.168.1.1").Value(&cfg.DefaultGateway),
		huh.NewInput().Title("Subnet prefix / CIDR").Placeholder("24").Value(&b.cidrStr).Validate(validateOptionalCIDR),
		huh.NewInput().Title("Dotfiles repo (optional, SSH URL)").
			Description("Enables the dotfiles software option; empty keeps it hidden.").
			Placeholder("git@github.com:you/dotfiles.git").Value(&cfg.DotfilesRepo),
	)
	if len(sshOpts) > 0 {
		fields = append(fields, huh.NewMultiSelect[string]().Title("SSH keys").
			Description("From ~/.ssh; injected into new VMs via cloud-init.").
			Options(sshOpts...).Value(&b.sshNames))
	}
	b.form = huh.NewForm(huh.NewGroup(fields...))
	return b, nil
}

// Save persists the edited configuration.
func (b *setupBinding) Save() error {
	b.cfg.DefaultCIDR = parseIntOr(b.cidrStr, 24)
	// Resolve selected SSH key names to their scanned entries.
	b.cfg.SSHKeys = b.cfg.SSHKeys[:0]
	for _, k := range b.scanned {
		if slices.Contains(b.sshNames, k.Name) {
			b.cfg.SSHKeys = append(b.cfg.SSHKeys, k)
		}
	}
	if b.cfg.DefaultSSHKey == "" && len(b.cfg.SSHKeys) > 0 {
		b.cfg.DefaultSSHKey = b.cfg.SSHKeys[0].Name
	}
	return b.cfg.Save()
}

// --- small helpers (local to keep internal/wizard untouched) ---

func validateNonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func validatePosInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return fmt.Errorf("enter a positive number")
	}
	return nil
}

func validateNonNegInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return fmt.Errorf("enter a non-negative number")
	}
	return nil
}

// validateMemMB validates a memory value: GB by default with an explicit M/MB
// suffix for the sub-GB case (e.g. "2", "0.5", "512M").
func validateMemMB(s string) error {
	_, err := plans.ParseMem(s)
	return err
}

// parseMBSafe parses a memory value to MB, returning 0 on error (the form's
// validator already rejects bad input before this runs).
func parseMBSafe(s string) int {
	mb, _ := plans.ParseMem(s)
	return mb
}

func validateHostname(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("use lowercase letters, digits and hyphens")
		}
	}
	return nil
}

func validateOptionalCIDR(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 32 {
		return fmt.Errorf("enter a prefix 1–32")
	}
	return nil
}

func atoiSafe(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func parseIntOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
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

func stringOpts(values []string) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(values))
	for _, v := range values {
		opts = append(opts, huh.NewOption(v, v))
	}
	return opts
}

func storageNameOpts(ss []proxmox.Storage) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(ss))
	for _, s := range ss {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", s.Name, s.Type), s.Name))
	}
	return opts
}

func nodeNames(ns []proxmox.Node) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

// templateNames returns the unique template names (templates may repeat across
// nodes, but the default template is stored by name).
func templateNames(ts []proxmox.Template) []string {
	out := make([]string, 0, len(ts))
	seen := map[string]bool{}
	for _, t := range ts {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t.Name)
	}
	return out
}
