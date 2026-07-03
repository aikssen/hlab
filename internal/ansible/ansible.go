// Package ansible materializes the embedded Ansible content into the homelab-state
// workspace and runs ansible-playbook to provision a VM with its selected software.
//
// hlab drives the official ansible-playbook binary; it does not reimplement it.
package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aikssen/hlab/assets"
	"github.com/aikssen/hlab/internal/state"
)

// Runner drives ansible-playbook inside a working directory (e.g. ~/.hlab/ansible).
type Runner struct {
	Dir     string
	Verbose int             // 0 = quiet (capture, show only on error); >=2 also passes -v to ansible
	Out     io.Writer       // when set, stream output here instead (used by the TUI log panel)
	Ctx     context.Context // when set, the ansible process is bound to it (cancellable)
	// Upgrade, when true, tells the playbook to also perform safe package/
	// runtime upgrades (apt upgrade, mise self-update + runtime upgrades) and
	// re-run the self-updating CLI installers, on top of the usual idempotent
	// reconcile. Used by Engine.Update(vm, upgrade=true).
	Upgrade bool
}

func New(dir string) *Runner { return &Runner{Dir: dir} }

// Available reports whether ansible-playbook is on PATH.
func Available() bool {
	_, err := exec.LookPath("ansible-playbook")
	return err == nil
}

// materialize writes the embedded playbook/tasks into the working directory.
func (r *Runner) materialize() error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	src, err := assets.Ansible()
	if err != nil {
		return err
	}
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(r.Dir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

// Provision runs the playbook against a single VM reachable at ip.
func (r *Runner) Provision(vm *state.VMSpec, ip, dotfilesRepo string) error {
	if !Available() {
		return fmt.Errorf("ansible-playbook not found in PATH (install it, e.g. `mise use -g pipx:ansible-core`)")
	}
	if ip == "" {
		return fmt.Errorf("no IP address known for %q yet — wait for boot and run `hlab vm list`", vm.Name)
	}
	if err := r.materialize(); err != nil {
		return err
	}

	// Inventory: a single host.
	inv := fmt.Sprintf("[all]\n%s ansible_host=%s ansible_user=%s\n", vm.Name, ip, vm.Username)
	invPath := filepath.Join(r.Dir, "inventory.ini")
	if err := os.WriteFile(invPath, []byte(inv), 0o644); err != nil {
		return err
	}

	// Extra vars. Coerce a nil software list to an empty list so the playbook's
	// `'x' in software` checks don't receive a JSON null.
	sw := vm.Software
	if sw == nil {
		sw = []string{}
	}
	vars := map[string]any{
		"software":      sw,
		"dotfiles_repo": dotfilesRepo,
		"target_user":   vm.Username,
		// LXC templates are minimal (no git/curl/sudo…); the playbook installs a
		// service-ready base set when this is true. VM golden images already have them.
		"is_lxc":  vm.IsLXC(),
		"upgrade": r.Upgrade,
	}
	varsData, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	varsPath := filepath.Join(r.Dir, "vars.json")
	if err := os.WriteFile(varsPath, varsData, 0o644); err != nil {
		return err
	}

	args := []string{"-i", "inventory.ini", "playbook.yml", "--extra-vars", "@vars.json"}
	if r.Verbose >= 2 {
		args = append(args, "-v")
	}
	var cmd *exec.Cmd
	if r.Ctx != nil {
		cmd = exec.CommandContext(r.Ctx, "ansible-playbook", args...)
	} else {
		cmd = exec.Command("ansible-playbook", args...)
	}
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"ANSIBLE_HOST_KEY_CHECKING=False",
		"ANSIBLE_RETRY_FILES_ENABLED=False",
		// Forward the local SSH agent so the VM can clone private repos (e.g.
		// dotfiles) using the operator's key, which never leaves the Mac.
		// Ignore the VM's host key (DHCP IPs get reused).
		"ANSIBLE_SSH_ARGS=-o ControlMaster=auto -o ControlPersist=60s "+
			"-o ForwardAgent=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null",
	)
	if r.Out != nil {
		cmd.Stdout, cmd.Stderr = r.Out, r.Out
		return cmd.Run()
	}
	if r.Verbose > 0 {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Stderr.Write(out)
	}
	return err
}
