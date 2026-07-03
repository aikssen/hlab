// Package engine holds hlab's presentation-free orchestration: it persists VM
// declarations and drives Terraform (lifecycle) and Ansible (provisioning) over
// the homelab-state store. Both the CLI (cmd) and the dashboard TUI (internal/
// tui) call into it, so the create/provision/destroy logic — including rollback
// of a partial VM — lives in exactly one place.
//
// The engine never prints or spins; callers wrap its (blocking) operations with
// whatever progress UI they use (a spinner in the CLI, an async log panel in the
// TUI). Output verbosity is controlled through the injected Terraform runner's
// Verbose field and the engine's AnsibleVerbose field.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aikssen/hlab/internal/ansible"
	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/software"
	"github.com/aikssen/hlab/internal/sshutil"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
	"github.com/aikssen/hlab/internal/wizard"
)

// Engine bundles the dependencies the orchestration needs.
type Engine struct {
	Cfg    *config.Config
	Store  *state.Store
	Runner Runner  // Terraform (see deps.go); caller sets Out/Ctx before long ops
	PM     Proxmox // Proxmox discovery + power/snapshots (see deps.go)

	// AnsibleVerbose is applied to the Ansible runner created per Provision call.
	AnsibleVerbose int
	// AnsibleOut, when set, streams Ansible output there (the TUI log panel).
	AnsibleOut io.Writer
	// Ctx, when set, binds long operations to a context so they can be cancelled.
	Ctx context.Context
}

// New builds an Engine from its dependencies. runner/pm are interfaces (see
// deps.go) so tests can inject fakes; production passes the concrete
// *terraform.Runner and *proxmox.Client.
func New(cfg *config.Config, store *state.Store, runner Runner, pm Proxmox) *Engine {
	return &Engine{Cfg: cfg, Store: store, Runner: runner, PM: pm}
}

// Create persists the declaration, rebuilds the Terraform workspace and creates
// the VM, rolling back a partially-created VM on failure. It returns the resolved
// IP. This is a blocking, long-running operation.
func (e *Engine) Create(res *wizard.Result) (string, error) {
	// Reject a VM ID that already exists in the cluster up front, with a clear
	// message — otherwise Terraform fails mid-apply with an opaque
	// "<vmid>.conf failed: File exists" and then rolls back.
	if err := e.vmidConflict(res.VM.VMID); err != nil {
		return "", err
	}
	// Reject a name that already has a declaration: create overwrites vms/<name>.yaml
	// in prepareWorkspace, and if the existing guest has a different vmid the next
	// Apply would plan a replace and -auto-approve would destroy it. (adopt guards
	// the same way.) Reuse the existing guest via update/adopt instead.
	if e.declarationExists(res.VM.Name) {
		return "", fmt.Errorf("a guest named %q is already managed by hlab — choose another name, or run `hlab %s update %s` to re-provision the existing guest", res.VM.Name, verbFor(res.VM), res.VM.Name)
	}

	// On Proxmox VE 9.1+ a bpg container's net0 defaults to host-managed=0, so
	// Proxmox does NOT write the network config inside the guest. With a static
	// IP nobody configures the interface and the container boots with no address
	// at all. Setting host_managed makes Proxmox own the config. DHCP containers
	// work regardless (the template's own networkd does DHCP). The attribute
	// requires PVE 9.1+, so gate it on the live version — older releases must
	// never receive the parameter. Best-effort: a failed version read (or an
	// unparseable version) simply leaves the flag unset.
	if res.VM.IsLXC() && !res.VM.DHCP {
		if v, verr := e.PM.Version(); verr == nil && pveVersionAtLeast(v, 9, 1) {
			res.VM.HostManagedNet = true
		}
	}

	if err := e.prepareWorkspace(res); err != nil {
		return "", err
	}

	if err := e.Runner.Apply(res.VM); err != nil {
		// Roll back a partially-created guest so a retry isn't blocked by a leftover
		// (e.g. "config file already exists"), and don't leave a dangling
		// declaration for a guest that was never fully created. The cleanup must run
		// even if the apply was cancelled, so detach it from the (cancelled) context.
		e.Runner.Detach()
		_ = e.Runner.Destroy(res.VM)
		_ = e.Store.Delete(res.VM.Name)
		remaining, _ := e.Store.List()
		pw, _ := e.Runner.ExistingPasswords()
		delete(pw, res.VM.Name)
		_ = e.Runner.Sync(remaining, pw)
		return "", fmt.Errorf("create failed (rolled back): %w", err)
	}

	// Ensure a static IP actually applied. On the first boot a VM may keep a DHCP
	// lease until cloud-init's interface rename (eth0) settles after a reboot. LXC
	// containers have no QEMU agent to poll, so this step is VM-only.
	if !res.VM.DHCP && !res.VM.IsLXC() {
		e.EnsureStaticApplied(res.VM)
	}

	_ = e.Store.Commit(fmt.Sprintf("%s: create %s", verbFor(res.VM), res.VM.Name))
	_ = e.Store.Push()

	// Remember the username so the next VM create defaults to it. LXC containers
	// always log in as root, so only VMs update the default. Best-effort: a failed
	// config save must not fail the (already-succeeded) create.
	if !res.VM.IsLXC() && res.VM.Username != "" && res.VM.Username != e.Cfg.DefaultUser {
		e.Cfg.DefaultUser = res.VM.Username
		_ = e.Cfg.Save()
	}

	return e.ResolveIP(res.VM), nil
}

// vmidConflict returns a descriptive error if vmid is already used by any guest
// in the cluster. Best-effort: if discovery fails it returns nil, so a transient
// API hiccup never blocks a create (the apply would still fail loudly later).
func (e *Engine) vmidConflict(vmid int) error {
	guests, err := e.PM.ClusterGuests()
	if err != nil {
		return nil
	}
	for _, g := range guests {
		if g.VMID == vmid {
			return fmt.Errorf("VM ID %d is already in use by %q (%s on %s) — choose another ID", vmid, g.Name, g.Type, g.Node)
		}
	}
	return nil
}

// declarationExists reports whether a declaration with this name is already in
// the store — used to reject a create/dry-run that would clobber a managed guest.
// It fails closed: a file that exists but won't parse (e.g. an interrupted
// Store.Save) still counts as "exists", so create can't silently overwrite it.
// Only a genuine not-exist frees the name.
func (e *Engine) declarationExists(name string) bool {
	_, err := e.Store.Load(name)
	return err == nil || !os.IsNotExist(err)
}

// verbFor returns the CLI/command-group verb for a guest ("vm" or "ct"), used in
// commit messages and user-facing hints.
func verbFor(vm *state.VMSpec) string {
	if vm.IsLXC() {
		return "ct"
	}
	return "vm"
}

// pveVersionAtLeast reports whether a Proxmox VE version string (e.g. "9.1.2",
// "8.4.1", or "9.0-4") is >= major.minor. Only the leading major and minor
// components are compared; any trailing patch/build suffix is ignored. An
// unparseable version returns false — fail closed, so a 9.1+-only flag is never
// set on an unknown version.
func pveVersionAtLeast(version string, major, minor int) bool {
	maj, min, ok := parseMajorMinor(version)
	if !ok {
		return false
	}
	if maj != major {
		return maj > major
	}
	return min >= minor
}

// parseMajorMinor extracts the leading major and minor integers from a Proxmox
// version string, tolerating "10.0", "9.1.2" and a "-build" suffix on the minor
// part ("9.0-4" → 9, 0). A missing minor defaults to 0. ok is false when the
// major component isn't a number.
func parseMajorMinor(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 2)
	maj, okMaj := leadingInt(parts[0])
	if !okMaj {
		return 0, 0, false
	}
	if len(parts) == 2 {
		minor, _ = leadingInt(parts[1])
	}
	return maj, minor, true
}

// leadingInt parses the leading run of ASCII digits from s (e.g. "0-4" → 0),
// reporting ok=false when there is no leading digit.
func leadingInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	return n, err == nil
}

// DryRun shows the Terraform plan for a would-be VM without applying it or
// persisting the declaration. The plan output streams (Runner.Plan always streams).
func (e *Engine) DryRun(res *wizard.Result) error {
	// Same guard as Create: without it, dry-running a name that already exists
	// overwrites its declaration in prepareWorkspace and the cleanup below then
	// deletes it outright — a "read-only" plan must never mutate a managed guest's
	// declaration.
	if e.declarationExists(res.VM.Name) {
		return fmt.Errorf("a guest named %q is already managed by hlab — choose another name, or run `hlab %s update %s` to re-provision the existing guest", res.VM.Name, verbFor(res.VM), res.VM.Name)
	}
	if err := e.prepareWorkspace(res); err != nil {
		return err
	}
	planErr := e.Runner.Plan()
	// Don't persist a declaration for a plan-only run.
	_ = e.Store.Delete(res.VM.Name)
	remaining, _ := e.Store.List()
	pw, _ := e.Runner.ExistingPasswords()
	_ = e.Runner.Sync(remaining, pw)
	return planErr
}

// prepareWorkspace persists the declaration and rebuilds + initializes the
// Terraform workspace from all declarations, preserving existing passwords.
func (e *Engine) prepareWorkspace(res *wizard.Result) error {
	if err := e.Store.Save(res.VM); err != nil {
		return err
	}
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if res.Password != "" {
		passwords[res.VM.Name] = res.Password
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}
	return e.Runner.Init()
}

// Provision installs the VM's currently-selected software and dotfiles via
// Ansible (the selection must already be set on vm). It persists the selection
// and commits the declaration. Blocking, long-running.
func (e *Engine) Provision(vm *state.VMSpec) error { return e.provision(vm, false, "provision") }

// Update re-runs Ansible against an already-provisioned guest using its saved
// software/dotfiles selection — no prompting, and unlike Provision it is not
// gated on the selection being non-empty (a "just the OS" guest is still
// reconcilable/updatable). When upgrade is true the playbook additionally
// applies safe package/runtime upgrades (apt upgrade, mise self-update +
// runtime upgrades) and re-runs the self-updating CLI installers. Blocking,
// long-running.
func (e *Engine) Update(vm *state.VMSpec, upgrade bool) error {
	return e.provision(vm, upgrade, "update")
}

// provision is the shared implementation behind Provision and Update: it
// persists the declaration, resolves the guest's IP, runs the Ansible
// playbook, and commits with a type- and verb-aware message (e.g.
// "vm: provision web" / "ct: update web") — fixing the previously hardcoded
// "vm: provision" commit message for containers.
func (e *Engine) provision(vm *state.VMSpec, upgrade bool, verbWord string) error {
	// Dotfiles is a catalog entry that needs a repo to clone; refuse clearly when
	// it is selected but none is configured, rather than failing deep in Ansible.
	if slices.Contains(vm.Software, software.DotfilesKey) && e.Cfg.DotfilesRepo == "" {
		return fmt.Errorf("dotfiles selected but no dotfiles_repo configured — set it with 'hlab setup --dotfiles-repo <ssh-url>'")
	}
	if err := e.Store.Save(vm); err != nil {
		return err
	}
	ip := e.ResolveIP(vm)

	ar := ansible.New(filepath.Join(e.Cfg.StateDirExpanded(), "ansible"))
	ar.Verbose = e.AnsibleVerbose
	ar.Out = e.AnsibleOut
	ar.Ctx = e.Ctx
	ar.Upgrade = upgrade
	if err := ar.Provision(vm, ip, e.Cfg.DotfilesRepo); err != nil {
		return err
	}
	_ = e.Store.Commit(fmt.Sprintf("%s: %s %s", verbFor(vm), verbWord, vm.Name))
	_ = e.Store.Push()
	return nil
}

// Destroy destroys the guest (VM or container) in Proxmox and removes its
// declaration, re-syncing the workspace without it. Blocking, long-running.
func (e *Engine) Destroy(name string) error {
	// Load the declaration so we can target the right resource (vm vs container).
	target, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	// Ensure the workspace includes the target before destroying it.
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}
	if err := e.Runner.Init(); err != nil {
		return err
	}
	if err := e.Runner.Destroy(target); err != nil {
		return err
	}

	if err := e.Store.Delete(name); err != nil {
		return err
	}
	delete(passwords, name)
	remaining, err := e.Store.List()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(remaining, passwords); err != nil {
		return err
	}

	_ = e.Store.Commit(fmt.Sprintf("%s: destroy %s", verbFor(target), name))
	_ = e.Store.Push()
	return nil
}

// migrateTimeout bounds how long the engine waits for an LXC migration task;
// copying the rootfs across nodes (local storage) can take several minutes.
const migrateTimeout = 20 * time.Minute

// Migrate moves a managed guest to another cluster node. The node is part of the
// declaration, so unlike power actions this is a lifecycle change. For a VM the
// move goes through Terraform (the vm resource sets migrate=true, so the bpg
// provider migrates it — online if running — instead of recreating it). For an LXC
// container, which the bpg resource can't migrate in place, hlab performs the move
// via the Proxmox API and then re-anchors the Terraform resource to the new node.
// Blocking, long-running. On failure the declaration is restored so it keeps
// matching reality.
func (e *Engine) Migrate(name, toNode string) error {
	if toNode == "" {
		return fmt.Errorf("target node is required")
	}
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if vm.Node == toNode {
		return fmt.Errorf("%s is already on node %q", name, toNode)
	}

	// Reject an unknown target up front with a clear message, rather than letting
	// the migration fail opaquely partway through.
	nodes, err := e.PM.Nodes()
	if err != nil {
		return err
	}
	nodeNames := make([]string, len(nodes))
	for i, n := range nodes {
		nodeNames[i] = n.Name
	}
	if !slices.Contains(nodeNames, toNode) {
		return fmt.Errorf("node %q not found in the cluster", toNode)
	}

	if vm.IsLXC() {
		return e.migrateContainer(vm, toNode)
	}

	from := vm.Node
	vm.Node = toNode
	if err := e.Store.Save(vm); err != nil {
		return err
	}
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}
	if err := e.Runner.Init(); err != nil {
		return err
	}
	if err := e.Runner.Apply(vm); err != nil {
		// The VM did not move; restore the declaration (and workspace) to the
		// original node so state, tfvars and reality stay in agreement.
		vm.Node = from
		_ = e.Store.Save(vm)
		remaining, _ := e.Store.List()
		pw, _ := e.Runner.ExistingPasswords()
		_ = e.Runner.Sync(remaining, pw)
		return fmt.Errorf("migrate failed (declaration restored): %w", err)
	}

	_ = e.Store.Commit(fmt.Sprintf("vm: migrate %s to %s", name, toNode))
	_ = e.Store.Push()
	return nil
}

// migrateContainer moves an LXC container via the Proxmox API (the bpg container
// resource has no migrate attribute) and then re-anchors the Terraform resource to
// the new node by patching node_name in state (SetResourceNode) — a plain apply
// would try to recreate it, so this keeps state/tfvars/reality in agreement
// without destroying the container.
func (e *Engine) migrateContainer(vm *state.VMSpec, toNode string) error {
	from := vm.Node

	// Pre-flight: Proxmox can't migrate a container that has snapshots on non-shared
	// (local) storage — LVM-thin snapshots aren't migratable. Fail clearly up front
	// instead of letting the migration shut the container down and then abort.
	if snaps, serr := e.PM.Snapshots(from, "lxc", vm.VMID); serr == nil && len(snaps) > 0 {
		if shared, _ := e.PM.StorageShared(from, vm.Storage); !shared {
			return fmt.Errorf("cannot migrate %q: it has %d snapshot(s) on non-shared storage %q, which Proxmox refuses to migrate. Delete them first (`hlab ct snapshots %s`, then `hlab ct snapshot-delete %s <snap>`)",
				vm.Name, len(snaps), vm.Storage, vm.Name, vm.Name)
		}
	}

	wasRunning := false
	if st, serr := e.PM.GuestStatus(from, "lxc", vm.VMID); serr == nil {
		wasRunning = st == "running"
	}

	// A running container can't live-migrate; restart=1 stops → migrates → starts.
	upid, err := e.PM.MigrateContainer(from, vm.VMID, toNode, wasRunning)
	if err != nil {
		return fmt.Errorf("migrate failed: %w", err)
	}
	if err := e.PM.WaitTask(from, upid, migrateTimeout); err != nil {
		return fmt.Errorf("migrate failed: %w", err)
	}

	// The container now lives on toNode. Reconcile Terraform: update the
	// declaration + tfvars (config), then re-anchor the resource's node_name in
	// state in place. We patch node_name rather than state rm + import so the
	// create-time attributes (vm_id, timeouts) survive — an imported container
	// comes back with those null and can no longer be updated or destroyed.
	vm.Node = toNode
	if err := e.Store.Save(vm); err != nil {
		return err
	}
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}
	if err := e.Runner.SetResourceNode(vm, toNode); err != nil {
		return fmt.Errorf("container migrated to %s, but re-anchoring Terraform state failed: %w", toNode, err)
	}

	_ = e.Store.Commit(fmt.Sprintf("ct: migrate %s to %s", vm.Name, toNode))
	_ = e.Store.Push()
	return nil
}

// Reconfigure applies an edited hardware spec (cores / memory / disk) to an
// existing managed guest: it persists the updated declaration, re-syncs the
// workspace and applies. The bpg provider updates the guest in place (cores/memory
// change the config; the disk can only grow — a smaller size is rejected up
// front). Works for both VMs and LXC containers. Blocking, long-running. On
// failure the previous declaration is restored so state/tfvars/reality stay in
// agreement.
func (e *Engine) Reconfigure(vm *state.VMSpec) error {
	old, err := e.Store.Load(vm.Name)
	if err != nil {
		return err
	}
	if vm.DiskGB < old.DiskGB {
		return fmt.Errorf("disk can only grow: %d GB is smaller than the current %d GB", vm.DiskGB, old.DiskGB)
	}
	if err := e.Store.Save(vm); err != nil {
		return err
	}
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}
	if err := e.Runner.Init(); err != nil {
		return err
	}
	if err := e.Runner.Apply(vm); err != nil {
		// Restore the previous declaration (and workspace) so nothing drifts.
		_ = e.Store.Save(old)
		remaining, _ := e.Store.List()
		pw, _ := e.Runner.ExistingPasswords()
		_ = e.Runner.Sync(remaining, pw)
		return fmt.Errorf("reconfigure failed (declaration restored): %w", err)
	}

	_ = e.Store.Commit(fmt.Sprintf("vm: reconfigure %s", vm.Name))
	_ = e.Store.Push()
	return nil
}

// AddSSHKey records an SSH public key on a managed guest's declaration and
// reconciles Terraform so no drift lingers. The key must already have been
// installed on the live guest's authorized_keys by the caller (cmd, over SSH) —
// this is the persistence + IaC-reconciliation half of `hlab {vm,ct}
// add-ssh-key`, kept here so the CLI stays thin. Idempotent: a key already in
// the declaration is a no-op (no duplicate, no apply, no commit). Blocking for
// VMs (runs a targeted plan + apply).
//
// Reconciliation is deliberately type-specific, to keep `hlab plan` clean and
// never risk a replace:
//   - VM: main.tf renders initialization.user_account.keys from the declaration
//     and does NOT lifecycle-ignore it, so a raw plan would report the new key
//     as in-place drift indefinitely. A targeted Apply pushes the key into
//     Terraform state (bpg updates cloud-init in place — never a replace),
//     leaving state == declaration. It is guarded by a PlanDetailed replace-veto
//     so an unexpected replace can never be auto-applied.
//   - LXC: container.tf's lifecycle ignore_changes covers
//     initialization[0].user_account, so Terraform never plans container SSH
//     keys. Sync keeps tfvars consistent with the declaration and there is
//     nothing to apply — `hlab plan` stays clean on its own.
func (e *Engine) AddSSHKey(vm *state.VMSpec, pubKey string) error {
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return fmt.Errorf("empty SSH public key")
	}
	if slices.Contains(vm.SSHKeys, pubKey) {
		return nil // already recorded — nothing to persist or reconcile
	}
	vm.SSHKeys = append(vm.SSHKeys, pubKey)
	if err := e.Store.Save(vm); err != nil {
		return err
	}
	vms, err := e.Store.List()
	if err != nil {
		return err
	}
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return err
	}
	if err := e.Runner.Sync(vms, passwords); err != nil {
		return err
	}

	// Containers: Terraform ignores initialization[0].user_account, so tfvars is
	// already consistent and there is nothing to apply. VMs need a targeted apply
	// to fold the key into state, guarded so it can never force a replace.
	if !vm.IsLXC() {
		if err := e.Runner.Init(); err != nil {
			return err
		}
		_, replace, summary, perr := e.Runner.PlanDetailed(vm)
		if perr != nil {
			return perr
		}
		if replace {
			return fmt.Errorf("refusing to apply: adding the key would force a replace of %s:\n%s", vm.Name, summary)
		}
		if err := e.Runner.Apply(vm); err != nil {
			return err
		}
	}

	_ = e.Store.Commit(fmt.Sprintf("%s: add ssh key to %s", verbFor(vm), vm.Name))
	_ = e.Store.Push()
	return nil
}

// InjectSSHKeyViaConsole seeds an SSH public key into a keyless LXC container over
// the Proxmox console (termproxy) and then persists it via AddSSHKey. This is the
// one way in for a container created with no SSH key: sshd refuses root password
// auth (PermitRootLogin prohibit-password), so hlab cannot reach it over SSH to
// install the first key — but the root password DOES work on the console. hlab
// logs in there, appends the key to /root/.ssh/authorized_keys and verifies it,
// after which ordinary SSH (and AddSSHKey's reconciliation) works.
//
// It requires the container's root password and the VM.Console privilege on the
// API token. The password normally comes from the gitignored secrets file (stored
// when the container was created), but that file is machine-local and never
// versioned, so it is absent for a container created on another machine (or by an
// older hlab): callers can supply it out of band via
// InjectSSHKeyViaConsoleWithPassword. When no password is available at all,
// KeylessAddKeyError explains the fix. Only valid for LXC; VM (serial/VNC)
// consoles use a different protocol and are out of scope.
func (e *Engine) InjectSSHKeyViaConsole(vm *state.VMSpec, pubKey string) error {
	password, err := e.StoredCTPassword(vm.Name)
	if err != nil {
		return err
	}
	return e.InjectSSHKeyViaConsoleWithPassword(vm, pubKey, password)
}

// InjectSSHKeyViaConsoleWithPassword is InjectSSHKeyViaConsole with the console
// login password supplied by the caller (rather than looked up in the secrets
// file), so an operator can recover a container whose root password never made it
// into this machine's local secrets file by entering it interactively. An empty
// password (none stored and none entered) still fails with KeylessAddKeyError.
func (e *Engine) InjectSSHKeyViaConsoleWithPassword(vm *state.VMSpec, pubKey, password string) error {
	if !vm.IsLXC() {
		return fmt.Errorf("console key injection is only supported for LXC containers")
	}
	if password == "" {
		return sshutil.KeylessAddKeyError(vm.Name, true)
	}
	script := []string{sshutil.AuthorizedKeyCommand(pubKey)}
	if err := e.PM.ConsoleExec(vm.Node, vm.VMID, script, proxmox.ConsoleLogin{User: "root", Password: password}); err != nil {
		return fmt.Errorf("injecting the key via the Proxmox console failed: %w", err)
	}
	return e.AddSSHKey(vm, pubKey)
}

// InjectSSHKeyViaAgent seeds an SSH public key into a keyless VM through the QEMU
// guest agent and then persists it via AddSSHKey. This is the one way in for a VM
// created with no SSH key: Ubuntu cloud images ship `PasswordAuthentication no`,
// so sshd refuses the cloud-init password, and hlab connects with key auth only —
// it can't reach the VM over SSH to install the first key. The guest agent runs as
// root inside the VM and needs no login, so hlab execs a small script there that
// appends the key to the connection user's ~/.ssh/authorized_keys (the key is fed
// on stdin via input-data, so it never has to be shell-quoted into the command),
// after which ordinary SSH — and AddSSHKey's Terraform reconciliation — works.
//
// It requires qemu-guest-agent running in the VM (hlab's golden images set
// agent=1) and the VM.GuestAgent.Unrestricted privilege on the API token; a 403 is
// surfaced naming that exact privilege. This is the VM analogue of
// InjectSSHKeyViaConsole — an LXC container has no guest agent, so it uses the
// console path instead and is rejected here.
func (e *Engine) InjectSSHKeyViaAgent(vm *state.VMSpec, pubKey string) error {
	if vm.IsLXC() {
		return fmt.Errorf("guest-agent key injection is only for VMs; an LXC container has no guest agent — it uses the Proxmox console")
	}
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return fmt.Errorf("empty SSH public key")
	}
	// Ping first so an agent that isn't up yields a clear message before we try to
	// exec (and so a missing privilege is reported once, up front).
	if err := e.PM.AgentPing(vm.Node, vm.VMID); err != nil {
		return err
	}
	// The key belongs to the account hlab connects as; the agent runs as root, so
	// the script chowns to that user. Fall back to the configured default, then root.
	user := vm.Username
	if user == "" {
		user = e.Cfg.DefaultUser
	}
	if user == "" {
		user = "root"
	}
	argv := []string{"/bin/sh", "-c", sshutil.AuthorizedKeyCommandFor(user)}
	res, err := e.PM.AgentExec(vm.Node, vm.VMID, argv, []byte(pubKey))
	if err != nil {
		return fmt.Errorf("injecting the key via the QEMU guest agent failed: %w", err)
	}
	if res.ExitCode != 0 {
		detail := strings.TrimSpace(res.ErrData)
		if detail == "" {
			detail = strings.TrimSpace(res.OutData)
		}
		return fmt.Errorf("injecting the key via the QEMU guest agent failed (exit %d): %s", res.ExitCode, detail)
	}
	return e.AddSSHKey(vm, pubKey)
}

// StoredCTPassword returns the container's root password from the gitignored
// secrets file, or "" when none is stored. Callers use it to decide whether they
// must obtain the password another way (e.g. prompt the operator) before a
// console key injection.
func (e *Engine) StoredCTPassword(name string) (string, error) {
	passwords, err := e.Runner.ExistingPasswords()
	if err != nil {
		return "", err
	}
	return passwords[name], nil
}

// DriftStatus is a managed guest's drift classification, as returned by
// DetectDrift. State is one of "in-sync" (no meaningful drift), "in-place" (an
// apply would change it in place), "replace" (an apply would destroy and
// recreate it), "missing" (it isn't in Terraform state at all, so an apply
// would create it) or "orphaned" (it's in Terraform state but has NO
// declaration — e.g. an adopt whose rollback couldn't `state rm` — so an
// untargeted apply would plan to destroy it; the live guest still exists).
type DriftStatus struct {
	Name, Kind, State string
	Attrs             []string
}

// DetectDrift runs a read-only `terraform plan` and classifies managed-guest
// drift, filtering out the provider/hlab bookkeeping noise a raw plan reports on
// every guest today (see terraform.driftIgnore/driftPaths). With no targets it
// covers the whole fleet; with one or more targets it scopes the plan (and the
// classified statuses) to just those guests — a faster `hlab plan <name>`. It
// mirrors the Reconfigure/Migrate preamble — Sync + Init — but never applies:
// Sync only rewrites tfvars from the current declarations, it doesn't touch
// state, and DriftReport itself only plans. The single code path used by both
// the CLI (`hlab plan`) and the TUI (`P`).
func (e *Engine) DetectDrift(targets ...*state.VMSpec) ([]DriftStatus, error) {
	vms, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	// Always Sync the full fleet so tfvars is complete for the plan, even when
	// the report itself is scoped to a target below.
	pw, _ := e.Runner.ExistingPasswords()
	if err := e.Runner.Sync(vms, pw); err != nil {
		return nil, err
	}
	if err := e.Runner.Init(); err != nil {
		return nil, err
	}
	changes, err := e.Runner.DriftReport(targets...)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]terraform.DriftChange, len(changes))
	for _, c := range changes {
		byName[c.Name] = c
	}

	// Classify the targets when scoped, else the whole fleet.
	classify := vms
	if len(targets) > 0 {
		classify = targets
	}
	statuses := make([]DriftStatus, 0, len(classify))
	declared := make(map[string]bool, len(vms))
	for _, vm := range vms {
		declared[vm.Name] = true
	}
	for _, vm := range classify {
		kind := "vm"
		if vm.IsLXC() {
			kind = "lxc"
		}
		st := DriftStatus{Name: vm.Name, Kind: kind, State: "in-sync"}
		if c, ok := byName[vm.Name]; ok {
			st.Attrs = c.Attrs
			switch c.Action {
			case "create":
				st.State = "missing"
			case "replace":
				st.State = "replace"
			default: // "update" (and, defensively, "delete")
				st.State = "in-place"
			}
		}
		statuses = append(statuses, st)
	}
	// A plan change for a resource with NO matching declaration is an orphan in
	// Terraform state (e.g. an adopt whose rollback couldn't `state rm`). Only a
	// whole-fleet plan can observe this (a scoped plan targets known declarations
	// by name), and it would plan to destroy the resource — surface it instead of
	// silently dropping it, so `hlab plan`/the TUI stay a trustworthy fleet audit.
	if len(targets) == 0 {
		for _, c := range changes {
			if declared[c.Name] {
				continue
			}
			kind := "vm"
			if c.IsLXC {
				kind = "lxc"
			}
			statuses = append(statuses, DriftStatus{Name: c.Name, Kind: kind, State: "orphaned", Attrs: c.Attrs})
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

// snapshotTimeout bounds how long the engine waits for a snapshot task; creating
// or rolling back with RAM state on a large VM can take a while.
const snapshotTimeout = 10 * time.Minute

// Snapshots lists a managed guest's snapshots (newest first). Read-only.
func (e *Engine) Snapshots(name string) ([]proxmox.Snapshot, error) {
	vm, err := e.Store.Load(name)
	if err != nil {
		return nil, err
	}
	return e.PM.Snapshots(vm.Node, vm.Kind(), vm.VMID)
}

// Snapshot creates a snapshot of a managed guest and waits for it to complete.
// withRAM captures the live memory (only valid for a running VM; ignored for
// containers). Snapshots are runtime state, not part of the declaration, so
// nothing is persisted.
func (e *Engine) Snapshot(name, snapName, description string, withRAM bool) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if vm.IsLXC() && withRAM {
		return fmt.Errorf("LXC snapshots cannot include RAM state")
	}
	upid, err := e.PM.CreateSnapshot(vm.Node, vm.Kind(), vm.VMID, snapName, description, withRAM)
	if err != nil {
		return err
	}
	return e.PM.WaitTask(vm.Node, upid, snapshotTimeout)
}

// RollbackSnapshot rolls a managed guest back to a snapshot and waits for it to
// complete. Changes made since the snapshot are discarded. If the guest was
// running, it is started again afterwards — a snapshot without live memory state
// (all LXC snapshots) otherwise leaves it stopped.
func (e *Engine) RollbackSnapshot(name, snapName string) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	wasRunning := false
	if st, serr := e.PM.GuestStatus(vm.Node, vm.Kind(), vm.VMID); serr == nil {
		wasRunning = st == "running"
	}
	upid, err := e.PM.RollbackSnapshot(vm.Node, vm.Kind(), vm.VMID, snapName, wasRunning)
	if err != nil {
		return err
	}
	return e.PM.WaitTask(vm.Node, upid, snapshotTimeout)
}

// DeleteSnapshot removes a snapshot of a managed guest and waits for it to complete.
func (e *Engine) DeleteSnapshot(name, snapName string) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	upid, err := e.PM.DeleteSnapshot(vm.Node, vm.Kind(), vm.VMID, snapName)
	if err != nil {
		return err
	}
	return e.PM.WaitTask(vm.Node, upid, snapshotTimeout)
}

// Start powers on the guest. Power state is runtime, not part of the declaration,
// so nothing is persisted or committed. Routes to the qemu/lxc path by type.
func (e *Engine) Start(name string) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	return e.PM.StartGuest(vm.Node, vm.Kind(), vm.VMID)
}

// Stop powers off the guest: a graceful shutdown by default, or a hard stop (cut
// power) when force is set. Like Start, it changes only runtime state.
func (e *Engine) Stop(name string, force bool) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if force {
		return e.PM.StopGuest(vm.Node, vm.Kind(), vm.VMID)
	}
	return e.PM.ShutdownGuest(vm.Node, vm.Kind(), vm.VMID)
}

// Reboot requests a graceful guest reboot. Like Start/Stop it is a runtime action
// and persists nothing.
func (e *Engine) Reboot(name string) error {
	vm, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	return e.PM.RebootGuest(vm.Node, vm.Kind(), vm.VMID)
}

// Guests lists every VM and LXC container in the cluster, used to refresh managed
// statuses and to surface guests not managed by hlab. Read-only discovery.
func (e *Engine) Guests() ([]proxmox.Guest, error) {
	return e.PM.ClusterGuests()
}

// StartGuest / StopGuest / RebootGuest are the power actions for discovered
// (unmanaged) guests, addressed by node + kind ("qemu"/"lxc") + vmid since they
// have no declaration in the store. Runtime only; nothing is persisted.
func (e *Engine) StartGuest(node, kind string, vmid int) error {
	return e.PM.StartGuest(node, kind, vmid)
}

func (e *Engine) StopGuest(node, kind string, vmid int, force bool) error {
	if force {
		return e.PM.StopGuest(node, kind, vmid)
	}
	return e.PM.ShutdownGuest(node, kind, vmid)
}

func (e *Engine) RebootGuest(node, kind string, vmid int) error {
	return e.PM.RebootGuest(node, kind, vmid)
}

// ResolveIP returns the best-known IP for a guest: the declared static IP, else a
// discovered address. VMs use the QEMU guest-agent (via terraform output,
// refreshing once if needed). LXC containers have no agent, but the host can read
// the container's namespace directly, so a DHCP container's address is still
// discoverable via ContainerIPv4s.
func (e *Engine) ResolveIP(vm *state.VMSpec) string {
	if ip := DeclaredIP(vm); ip != "" {
		return ip
	}
	if vm.IsLXC() {
		if ips, err := e.PM.ContainerIPv4s(vm.Node, vm.VMID); err == nil {
			return FirstIPv4(ips)
		}
		return ""
	}
	ip := FirstIPv4(e.Runner.IPAddresses()[vm.Name])
	if ip == "" {
		_ = e.Runner.Refresh()
		ip = FirstIPv4(e.Runner.IPAddresses()[vm.Name])
	}
	return ip
}

// EnsureStaticApplied makes sure a static-IP VM actually came up on its address.
// Ubuntu cloud images rename the NIC to eth0 only after a reboot, so the first
// boot can linger on a DHCP lease. If the static IP isn't present shortly, reboot
// once and wait for it.
func (e *Engine) EnsureStaticApplied(vm *state.VMSpec) {
	target := DeclaredIP(vm)
	if target == "" {
		return
	}
	hasStatic := func() bool {
		ips, err := e.PM.AgentIPv4s(vm.Node, vm.VMID)
		if err != nil {
			return false
		}
		return slices.Contains(ips, target)
	}
	if waitUntil(hasStatic, 30*time.Second, 3*time.Second) {
		return
	}
	_ = e.PM.RebootVM(vm.Node, vm.VMID)
	waitUntil(hasStatic, 150*time.Second, 5*time.Second)
}

func waitUntil(cond func() bool, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// DeclaredIP returns the static IP (without prefix) from the declaration, or ""
// for DHCP VMs. For static VMs this is authoritative and avoids reporting the
// transient DHCP lease a guest may hold briefly during first boot.
func DeclaredIP(vm *state.VMSpec) string {
	if !vm.DHCP && vm.IPCIDR != "" {
		return strings.SplitN(vm.IPCIDR, "/", 2)[0]
	}
	return ""
}

// FirstIPv4 returns the first non-loopback IPv4 address from a guest-agent list.
func FirstIPv4(addrs []string) string {
	for _, a := range addrs {
		if a == "" || strings.HasPrefix(a, "127.") || strings.Contains(a, ":") {
			continue
		}
		return a
	}
	return ""
}
