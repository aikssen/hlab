// Package state manages the homelab-state working copy (default ~/.hlab): the
// versioned, declarative description of every VM hlab manages.
//
// Layout:
//
//	~/.hlab/
//	  config.yaml              connection + secrets — GITIGNORED (contains the token)
//	  plans.yaml               VM/LXC plans — versioned
//	  vms/<name>.yaml          declaration — VERSIONED (source of truth)
//	  terraform/               generated terraform workspace
//	    terraform.tfvars.json    specs — versioned
//	    secrets.auto.tfvars.json passwords — GITIGNORED
//	    *.tfstate, .terraform/   operational — GITIGNORED
//	  .gitignore
//
// Secrets (VM passwords) are never written into the declaration YAML; only a
// HasPassword flag is recorded.
package state

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// VMSpec is the declarative description of a single guest (VM or LXC container).
type VMSpec struct {
	Name string `yaml:"name"` // hostname = guest name = terraform key = inventory name

	// Type discriminates the guest kind: "" or "vm" == QEMU VM (back-compat with
	// existing declarations that omit it); "lxc" == LXC container.
	Type string `yaml:"type,omitempty"`

	Node       string `yaml:"node"`
	VMID       int    `yaml:"vmid"`
	Template   string `yaml:"template"`    // human-readable template name
	TemplateID int    `yaml:"template_id"` // source VM id to clone (VMs only)
	Storage    string `yaml:"storage"`
	Bridge     string `yaml:"bridge"`

	// LXC-only fields (ignored for VMs).
	TemplateFile string `yaml:"template_file,omitempty"` // vztmpl volid, e.g. local:vztmpl/debian-12-standard_...tar.zst
	OSType       string `yaml:"os_type,omitempty"`       // bpg operating_system.type: debian|ubuntu|alpine|...
	Unprivileged bool   `yaml:"unprivileged,omitempty"`
	Nesting      bool   `yaml:"nesting,omitempty"`
	SwapMB       int    `yaml:"swap_mb,omitempty"`
	// HostManagedNet: PVE 9.1+ — Proxmox writes the container's network config
	// itself. Required for static-IP CTs there, where the bpg default
	// (host-managed=0) means nobody configures the interface and the guest boots
	// with no IP.
	HostManagedNet bool `yaml:"host_managed_net,omitempty"`

	// CPUType is the QEMU CPU model (VMs only — a container shares the host
	// kernel). Empty means the Terraform default, a portable baseline chosen so a
	// VM can live-migrate between nodes with different host CPUs. Worth overriding
	// per cluster: the default exposes AES but NOT PCLMULQDQ, and a binary compiled
	// to require it dies at startup with SIGILL. Proxmox won't let a single flag be
	// added on top of a model (its `flags` field only takes a security/virt subset),
	// so the whole model has to change — e.g. `EPYC` on an all-AMD cluster.
	CPUType string `yaml:"cpu_type,omitempty"`

	Plan     string `yaml:"plan,omitempty"` // preconfigured plan name (e.g. "KVM2"/"micro"), empty = custom
	Cores    int    `yaml:"cores"`
	MemoryGB int    `yaml:"memory_gb"`           // VM memory (GB)
	MemoryMB int    `yaml:"memory_mb,omitempty"` // LXC memory (MB); sub-GB tiers need this
	DiskGB   int    `yaml:"disk_gb"`

	DHCP    bool     `yaml:"dhcp"`
	IPCIDR  string   `yaml:"ip_cidr,omitempty"`
	Gateway string   `yaml:"gateway,omitempty"`
	DNS     []string `yaml:"dns,omitempty"`

	Username    string   `yaml:"username"`
	SSHKeys     []string `yaml:"ssh_keys,omitempty"` // public key contents
	HasPassword bool     `yaml:"has_password"`       // secret stored separately

	// Dotfiles exists only for legacy-YAML migration: dotfiles is an ordinary
	// software-catalog entry now, but declarations written before that still say
	// `dotfiles: true`. This field must stay so Store.Load can read them (yaml.v3
	// silently drops unknown keys, so removing it would lose the selection);
	// Store.Load migrates a legacy `dotfiles: true` into Software ("dotfiles") and
	// zeroes this, so all downstream code reads only Software, and omitempty keeps
	// it out of newly-written declarations.
	Dotfiles bool     `yaml:"dotfiles,omitempty"`
	Software []string `yaml:"software,omitempty"` // catalog keys, e.g. ["docker","node"]
}

// Kind maps the guest type to the Proxmox API path segment ("qemu" or "lxc").
func (v *VMSpec) Kind() string {
	if v.Type == "lxc" {
		return "lxc"
	}
	return "qemu"
}

// IsLXC reports whether this declaration describes an LXC container.
func (v *VMSpec) IsLXC() bool { return v.Type == "lxc" }

// Store is the homelab-state directory.
type Store struct {
	Dir string
}

func New(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) vmsDir() string       { return filepath.Join(s.Dir, "vms") }
func (s *Store) TerraformDir() string { return filepath.Join(s.Dir, "terraform") }

// RequiredGitignore lists the entries that must be present in the state dir's
// .gitignore. config.yaml carries the Proxmox token; the rest are operational or
// secret terraform artifacts. Kept in dependency order (config first) so a freshly
// created file reads sensibly.
var RequiredGitignore = []string{
	"config.yaml",
	"terraform/secrets.auto.tfvars.json",
	"terraform/*.tfstate",
	"terraform/*.tfstate.*",
	"terraform/.terraform/",
	"terraform/.terraform.lock.hcl",
	".env",
}

// EnsureGitignore makes sure every entry in required is present in dir/.gitignore,
// appending only the missing ones so an operator's own additions (and entries
// already there) are left untouched — never rewriting the whole file. A brand-new
// file gets a header comment. Idempotent.
func EnsureGitignore(dir string, required []string) error {
	gi := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(gi)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	present := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, line := range required {
		if !present[line] {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var buf strings.Builder
	if len(data) == 0 {
		buf.WriteString("# operational / secret — never commit\n")
	} else {
		buf.Write(data)
		if data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	for _, line := range missing {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return os.WriteFile(gi, []byte(buf.String()), 0o644)
}

// Init creates the directory layout and a .gitignore, and initializes git if the
// directory is not yet a repository.
func (s *Store) Init() error {
	for _, d := range []string{s.vmsDir(), s.TerraformDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := EnsureGitignore(s.Dir, RequiredGitignore); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".git")); os.IsNotExist(err) {
		if _, err := exec.LookPath("git"); err == nil {
			cmd := exec.Command("git", "init", "-q")
			cmd.Dir = s.Dir
			_ = cmd.Run()
		}
	}
	return nil
}

// Save writes a VM declaration to vms/<name>.yaml.
func (s *Store) Save(vm *VMSpec) error {
	if vm.Name == "" {
		return fmt.Errorf("vm has no name")
	}
	if err := os.MkdirAll(s.vmsDir(), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(vm)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.vmsDir(), vm.Name+".yaml"), data, 0o644)
}

// Load reads a single VM declaration by name.
func (s *Store) Load(name string) (*VMSpec, error) {
	data, err := os.ReadFile(filepath.Join(s.vmsDir(), name+".yaml"))
	if err != nil {
		return nil, err
	}
	var vm VMSpec
	if err := yaml.Unmarshal(data, &vm); err != nil {
		return nil, err
	}
	// Migrate a legacy Dotfiles bool into the software catalog: dotfiles is an
	// ordinary catalog entry now. The next Save persists the migrated form.
	if vm.Dotfiles {
		if !slices.Contains(vm.Software, "dotfiles") {
			vm.Software = append(vm.Software, "dotfiles")
		}
		vm.Dotfiles = false
	}
	return &vm, nil
}

// List returns all VM declarations, sorted by name.
func (s *Store) List() ([]*VMSpec, error) {
	entries, err := os.ReadDir(s.vmsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var vms []*VMSpec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		vm, err := s.Load(name)
		if err != nil {
			return nil, err
		}
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}

// Delete removes a VM declaration file.
func (s *Store) Delete(name string) error {
	return os.Remove(filepath.Join(s.vmsDir(), name+".yaml"))
}

// Commit stages everything and commits with the given message. Best-effort: it
// is a no-op when git is unavailable or there is nothing to commit.
func (s *Store) Commit(message string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".git")); os.IsNotExist(err) {
		return nil
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = s.Dir
	if err := add.Run(); err != nil {
		return err
	}
	commit := exec.Command("git", "commit", "-q", "-m", message)
	commit.Dir = s.Dir
	// Ignore "nothing to commit" exit status.
	_ = commit.Run()
	return nil
}

// Push pushes to the configured remote, if any. Best-effort.
func (s *Store) Push() error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	out, err := runOut(s.Dir, "git", "remote")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil // no remote configured yet
	}
	push := exec.Command("git", "push", "-q")
	push.Dir = s.Dir
	return push.Run()
}

func runOut(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
