// Adopt: convert a discovered (unmanaged) guest into a managed one by
// synthesizing a declaration from its live Proxmox config and importing it into
// Terraform state. The live guest is NEVER modified or destroyed by any of
// this — every failure path rolls back hlab's own artifacts (declaration,
// tfvars, state entry) and leaves the guest exactly as it was found.

package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
	"github.com/aikssen/hlab/internal/wizard"
)

// AdoptOptions carries the operator-editable knobs for BuildAdoptSpec: an
// override for the (kebabified) declaration name, and — VMs only — the SSH
// connection username to use when the live guest has no ciuser set (containers
// always log in as root, regardless of this field).
type AdoptOptions struct {
	Name     string
	Username string
}

// FindAdoptable resolves arg — a numeric vmid or an exact guest name — to a
// live, unmanaged guest of the requested kind ("qemu" or "lxc"; "" accepts
// either). Read-only: it never mutates anything. Rejects templates, guests
// hlab already manages, and (when wantKind is set) a kind mismatch, each with
// an actionable error message.
func (e *Engine) FindAdoptable(arg, wantKind string) (*proxmox.Guest, error) {
	guests, err := e.PM.ClusterGuests()
	if err != nil {
		return nil, err
	}
	managed, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	managedNames := make(map[int]string, len(managed))
	for _, m := range managed {
		managedNames[m.VMID] = m.Name
	}

	var g *proxmox.Guest
	if vmid, cerr := strconv.Atoi(strings.TrimSpace(arg)); cerr == nil {
		for i := range guests {
			if guests[i].VMID == vmid {
				g = &guests[i]
				break
			}
		}
		if g == nil {
			return nil, fmt.Errorf("no guest with vmid %d found in the cluster", vmid)
		}
	} else {
		var matches []*proxmox.Guest
		for i := range guests {
			if guests[i].Name == arg {
				matches = append(matches, &guests[i])
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("no guest named %q found in the cluster", arg)
		case 1:
			g = matches[0]
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = fmt.Sprintf("%d (%s on %s)", m.VMID, m.Type, m.Node)
			}
			return nil, fmt.Errorf("%d guests are named %q — specify the vmid instead: %s",
				len(matches), arg, strings.Join(ids, ", "))
		}
	}

	if g.Template {
		return nil, fmt.Errorf("%d (%s) is a template, not a guest — nothing to adopt", g.VMID, g.Name)
	}
	if name, ok := managedNames[g.VMID]; ok {
		return nil, fmt.Errorf("%d (%s) is already managed as %q", g.VMID, g.Name, name)
	}
	if wantKind != "" && g.Type != wantKind {
		if g.Type == "lxc" {
			return nil, fmt.Errorf("%d (%s) is an LXC container — use `hlab ct adopt %d`", g.VMID, g.Name, g.VMID)
		}
		return nil, fmt.Errorf("%d (%s) is a VM — use `hlab vm adopt %d`", g.VMID, g.Name, g.VMID)
	}
	return g, nil
}

// BuildAdoptSpec synthesizes a VMSpec (ready for Adopt) from a live guest's
// Proxmox config, plus a list of human-readable warnings about what the first
// managed apply will change. It rejects — without writing or importing
// anything — configurations hlab's Terraform can't safely represent: a live
// guest whose shape doesn't match what main.tf/container.tf render would
// either lose resources silently or force a replace once adopted.
func (e *Engine) BuildAdoptSpec(g proxmox.Guest, opts AdoptOptions) (*state.VMSpec, []string, error) {
	isLXC := g.Type == "lxc"

	var (
		cfg *proxmox.GuestConfig
		err error
	)
	if isLXC {
		cfg, err = e.PM.ContainerConfig(g.Node, g.VMID)
	} else {
		cfg, err = e.PM.VMConfig(g.Node, g.VMID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading config for %d: %w", g.VMID, err)
	}

	// Preflight: reject shapes hlab's Terraform can't safely represent, before
	// writing or importing anything.
	if isLXC {
		if len(cfg.MountPoints) > 0 {
			return nil, nil, fmt.Errorf("%d has extra mount point(s) (%s) hlab doesn't declare — remove them first",
				g.VMID, strings.Join(cfg.MountPoints, ", "))
		}
	} else {
		if cfg.BootDiskIface != "" && cfg.BootDiskIface != "scsi0" {
			return nil, nil, fmt.Errorf("%d's boot disk is on %q, not scsi0 — hlab only manages VMs booting from scsi0",
				g.VMID, cfg.BootDiskIface)
		}
		if len(cfg.ExtraDisks) > 0 {
			return nil, nil, fmt.Errorf("%d has extra disk(s) (%s) hlab doesn't declare — remove them first",
				g.VMID, strings.Join(cfg.ExtraDisks, ", "))
		}
	}
	if len(cfg.ExtraNICs) > 0 {
		return nil, nil, fmt.Errorf("%d has extra network interface(s) (%s) — hlab only manages net0",
			g.VMID, strings.Join(cfg.ExtraNICs, ", "))
	}

	name := kebabify(g.Name)
	if opts.Name != "" {
		name = kebabify(opts.Name)
	}
	if !kebabRe.MatchString(name) {
		return nil, nil, fmt.Errorf("%q is not a usable declaration name — pass --name with a lowercase, kebab-case hostname", name)
	}
	if _, lerr := e.Store.Load(name); lerr == nil {
		return nil, nil, fmt.Errorf("a declaration named %q already exists — pass --name to adopt %d under a different name", name, g.VMID)
	}

	spec := &state.VMSpec{
		Name:        name,
		Node:        g.Node,
		VMID:        g.VMID,
		Template:    "adopted",
		TemplateID:  0,
		Storage:     cfg.Storage,
		Bridge:      cfg.Bridge,
		Cores:       cfg.Cores,
		DiskGB:      cfg.DiskGB,
		DHCP:        cfg.DHCP,
		IPCIDR:      cfg.IPCIDR,
		Gateway:     cfg.Gateway,
		DNS:         cfg.DNS,
		HasPassword: false, // adopt never reads or sets the live guest's password
	}

	var warnings []string
	if name != g.Name {
		warnings = append(warnings, fmt.Sprintf("declared name %q differs from the live name %q — the next apply renames it", name, g.Name))
	}
	if g.Status != "running" {
		warnings = append(warnings, fmt.Sprintf("%d is currently stopped — the next apply starts it", g.VMID))
	}

	if isLXC {
		spec.Type = "lxc"
		spec.TemplateFile = "" // already created; container.tf skips operating_system
		spec.OSType = cfg.OSType
		spec.Unprivileged = cfg.Unprivileged
		spec.Nesting = cfg.Nesting
		spec.HostManagedNet = cfg.HostManaged // net0 host-managed=1 (PVE 9.1+); carried so it doesn't drift
		spec.SwapMB = cfg.SwapMB
		spec.MemoryMB = cfg.MemoryMB
		spec.Username = "root" // container login is always root; no ciuser concept
		// No SSHKeys: unreadable via the API, and container.tf's
		// lifecycle.ignore_changes covers initialization[0].user_account.
	} else {
		spec.Username = cfg.CIUser
		if spec.Username == "" {
			spec.Username = opts.Username
		}
		if spec.Username == "" {
			spec.Username = "root"
		}
		spec.SSHKeys = cfg.SSHKeys

		// Whole gigabytes go in MemoryGB like every created VM; odd sizes (e.g.
		// 2560 MB) keep the exact MB value, which writeTfvars prefers when set.
		if cfg.MemoryMB%1024 == 0 {
			spec.MemoryGB = cfg.MemoryMB / 1024
		} else {
			spec.MemoryMB = cfg.MemoryMB
		}

		if !cfg.AgentEnabled {
			warnings = append(warnings, "the QEMU guest agent is not enabled — the next apply turns it on (until then hlab can't discover its IP)")
		}
		if cfg.DHCP {
			warnings = append(warnings, "no static ipconfig0 is set — the next apply attaches cloud-init with DHCP; set a static IP afterwards if desired")
		}
		if cfg.Sockets > 1 {
			warnings = append(warnings, fmt.Sprintf("this VM has %d CPU sockets — hlab only declares cores (single socket); the next apply reduces it to 1", cfg.Sockets))
		}
	}

	return spec, warnings, nil
}

// Adopt brings a discovered (unmanaged) guest under hlab's control: it saves
// the synthesized declaration, imports the live guest into Terraform state at
// the address hlab would normally create it under, patches back the
// config-only attributes the import leaves null (terraform.AdoptedStateAttrs),
// and verifies with a targeted plan that nothing would force a replace before
// declaring success. The live guest is never modified by any of this — on any
// failure the adoption is rolled back (state rm best-effort + declaration
// deleted + workspace resynced) and the guest is left exactly as it was found.
// Blocking, long-running, like Create/Migrate/Reconfigure.
//
// Returns a non-empty drift summary when the guest matches the declaration
// except for in-place changes (e.g. agent/cloud-init/machine-type attributes
// the next apply will set) — callers should surface it as a warning, since it
// means the next real apply has work to do.
func (e *Engine) Adopt(vm *state.VMSpec) (drift string, err error) {
	// prepareWorkspace = Save → List → ExistingPasswords → Sync → Init, shared
	// with Create; adopt never sets a password, so Result.Password stays "".
	if err := e.prepareWorkspace(&wizard.Result{VM: vm}); err != nil {
		e.Runner.Detach()
		e.rollbackDeclaration(vm.Name)
		return "", fmt.Errorf("adopt failed preparing the workspace (declaration rolled back, guest untouched): %w", err)
	}

	if err := e.Runner.Import(vm); err != nil {
		// Nothing landed in Terraform state — only the declaration needs undoing.
		e.Runner.Detach()
		e.rollbackDeclaration(vm.Name)
		return "", fmt.Errorf("import failed (declaration rolled back, guest untouched): %w", err)
	}

	if err := e.Runner.PatchResourceAttrs(vm, terraform.AdoptedStateAttrs(vm)); err != nil {
		e.Runner.Detach()
		return "", fmt.Errorf("adopt failed patching state (%s): %w", e.adoptRolledBack(vm), err)
	}

	changes, replace, summary, perr := e.Runner.PlanDetailed(vm)
	if perr != nil {
		e.Runner.Detach()
		return "", fmt.Errorf("adopt failed verifying the plan (%s): %w", e.adoptRolledBack(vm), perr)
	}
	if replace {
		e.Runner.Detach()
		return "", fmt.Errorf("adopting %s would force a replace on the next apply — %s:\n%s", vm.Name, e.adoptRolledBack(vm), summary)
	}

	_ = e.Store.Commit(fmt.Sprintf("%s: adopt %s", verbFor(vm), vm.Name))
	_ = e.Store.Push()

	if changes {
		return summary, nil
	}
	return "", nil
}

// rollbackDeclaration undoes the workspace-preparation step of Adopt: it
// deletes the (just-written) declaration and resyncs the Terraform workspace
// without it. Best-effort, mirroring Create's inline rollback — the live guest
// itself is never touched (and NEVER Runner.Destroy'ed).
func (e *Engine) rollbackDeclaration(name string) {
	_ = e.Store.Delete(name)
	remaining, _ := e.Store.List()
	pw, _ := e.Runner.ExistingPasswords()
	_ = e.Runner.Sync(remaining, pw)
}

// rollbackAdoption undoes an adoption that reached (or passed) Import: it
// removes the resource from Terraform state — never Runner.Destroy, which
// would delete the live guest — then falls back to rollbackDeclaration. The
// declaration cleanup is best-effort, but the StateRm error is returned: if it
// fails, the resource is left in Terraform state with no matching declaration
// (an orphan a later untargeted apply would plan to destroy), and the caller
// must surface that rather than claiming a clean rollback.
func (e *Engine) rollbackAdoption(vm *state.VMSpec) error {
	rmErr := e.Runner.StateRm(vm)
	e.rollbackDeclaration(vm.Name)
	return rmErr
}

// adoptRolledBack rolls a partial adoption back and returns a human-readable
// description of the outcome to splice into the failure message. The live guest
// is never touched either way; the distinction is whether the *local* Terraform
// state was fully cleaned up.
func (e *Engine) adoptRolledBack(vm *state.VMSpec) string {
	if rmErr := e.rollbackAdoption(vm); rmErr != nil {
		return fmt.Sprintf("ROLLBACK INCOMPLETE — `terraform state rm` failed (%v): the live guest is untouched, but %q remains in Terraform state with no declaration; remove it (e.g. `terraform state rm` in %s, or re-run `hlab plan` which now reports it as orphaned) before any untargeted apply, or it would be planned for destroy", rmErr, vm.Name, e.Store.TerraformDir())
	}
	return "rolled back, guest untouched"
}

var kebabRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// kebabify lowercases a name, replaces any character outside [a-z0-9-] with
// '-', then squeezes repeated hyphens and trims them from both ends — turning
// a live guest's name into a usable hlab declaration name.
func kebabify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
