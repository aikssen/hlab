// Package terraform materializes the embedded Terraform workspace into the
// homelab-state directory, renders the tfvars from VM declarations, and drives
// the terraform binary (init/plan/apply/destroy/output).
//
// Proxmox credentials are passed via PROXMOX_VE_* environment variables so they
// never get written to disk in the versioned workspace.
package terraform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aikssen/hlab/assets"
	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/state"
)

// Runner drives terraform inside a working directory.
type Runner struct {
	Dir     string // the terraform working directory (e.g. ~/.hlab/terraform)
	Cfg     *config.Config
	Verbose int             // 0 = quiet (capture, show only on error); >0 = stream output
	Out     io.Writer       // when set, stream output here instead (used by the TUI log panel)
	Ctx     context.Context // when set, the terraform process is bound to it (cancellable)
}

// command builds a terraform exec.Cmd bound to the runner's context when set.
func (r *Runner) command(args ...string) *exec.Cmd {
	if r.Ctx != nil {
		return exec.CommandContext(r.Ctx, "terraform", args...)
	}
	return exec.Command("terraform", args...)
}

func New(dir string, cfg *config.Config) *Runner {
	return &Runner{Dir: dir, Cfg: cfg}
}

// SetOut sets the writer long operations stream to (nil = quiet/captured). Lets a
// caller drive streaming through the engine.Runner interface without touching the
// Out field directly.
func (r *Runner) SetOut(w io.Writer) { r.Out = w }

// SetCtx binds long operations to ctx for cancellation. Used by the TUI to make an
// op cancellable.
func (r *Runner) SetCtx(ctx context.Context) { r.Ctx = ctx }

// Detach unbinds the cancellation context so a subsequent cleanup/rollback still
// runs even after the original operation was cancelled.
func (r *Runner) Detach() { r.Ctx = nil }

// tfVM mirrors the object type expected by var.vms in variables.tf.
type tfVM struct {
	Node       string   `json:"node"`
	VMID       int      `json:"vmid"`
	TemplateID int      `json:"template_id"`
	Storage    string   `json:"storage"`
	Bridge     string   `json:"bridge"`
	Cores      int      `json:"cores"`
	MemoryMB   int      `json:"memory_mb"`
	DiskGB     int      `json:"disk_gb"`
	DHCP       bool     `json:"dhcp"`
	IPCIDR     string   `json:"ip_cidr"`
	Gateway    string   `json:"gateway"`
	DNSServers []string `json:"dns_servers"`
	Username   string   `json:"username"`
	SSHKeys    []string `json:"ssh_keys"`
	// CPUType renders only when set (omitempty): unset → key absent → the
	// optional() default in variables.tf applies. That keeps an existing
	// declaration, written before cpu_type existed, rendering exactly as before
	// instead of planning a change.
	CPUType string `json:"cpu_type,omitempty"`
}

// tfCT mirrors the object type expected by var.cts in variables.tf.
type tfCT struct {
	Node         string `json:"node"`
	VMID         int    `json:"vmid"`
	TemplateFile string `json:"template_file"`
	OSType       string `json:"os_type"`
	Storage      string `json:"storage"`
	Bridge       string `json:"bridge"`
	Cores        int    `json:"cores"`
	MemoryMB     int    `json:"memory_mb"`
	SwapMB       int    `json:"swap_mb"`
	DiskGB       int    `json:"disk_gb"`
	Unprivileged bool   `json:"unprivileged"`
	Nesting      bool   `json:"nesting"`
	// HostManaged renders only when true (omitempty): unset → key absent →
	// optional(bool) yields null, so the PVE 9.1+ host_managed parameter is
	// never sent to an older Proxmox. hlab only ever needs to opt in.
	HostManaged bool     `json:"host_managed,omitempty"`
	DHCP        bool     `json:"dhcp"`
	IPCIDR      string   `json:"ip_cidr"`
	Gateway     string   `json:"gateway"`
	DNSServers  []string `json:"dns_servers"`
	SSHKeys     []string `json:"ssh_keys"`
}

// resourceAddr returns the terraform resource address for a guest, used to target
// a single VM or container in apply/destroy.
func resourceAddr(s *state.VMSpec) string {
	if s.IsLXC() {
		return `proxmox_virtual_environment_container.ct["` + s.Name + `"]`
	}
	return `proxmox_virtual_environment_vm.vm["` + s.Name + `"]`
}

// Sync writes the embedded workspace files and regenerates tfvars from the full
// set of VM declarations. passwords maps VM name -> cloud-init password (only the
// entries that actually have one); it is written to a gitignored secrets file.
func (r *Runner) Sync(vms []*state.VMSpec, passwords map[string]string) error {
	if err := r.materialize(); err != nil {
		return err
	}
	if err := r.writeTfvars(vms); err != nil {
		return err
	}
	// Partition passwords by guest type so each lands in the right tfvars key.
	vmPw, ctPw := map[string]string{}, map[string]string{}
	byName := map[string]*state.VMSpec{}
	for _, s := range vms {
		byName[s.Name] = s
	}
	for name, pw := range passwords {
		if s, ok := byName[name]; ok && s.IsLXC() {
			ctPw[name] = pw
		} else {
			vmPw[name] = pw
		}
	}
	return r.writeSecrets(vmPw, ctPw)
}

// materialize copies the embedded *.tf files into the working directory,
// overwriting them so the workspace always matches the installed hlab version.
func (r *Runner) materialize() error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	src, err := assets.Terraform()
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

func (r *Runner) writeTfvars(specs []*state.VMSpec) error {
	vms := map[string]tfVM{}
	cts := map[string]tfCT{}
	for _, s := range specs {
		if s.IsLXC() {
			mem := s.MemoryMB
			if mem == 0 {
				mem = s.MemoryGB * 1024
			}
			cts[s.Name] = tfCT{
				Node:         s.Node,
				VMID:         s.VMID,
				TemplateFile: s.TemplateFile,
				OSType:       s.OSType,
				Storage:      s.Storage,
				Bridge:       s.Bridge,
				Cores:        s.Cores,
				MemoryMB:     mem,
				SwapMB:       s.SwapMB,
				DiskGB:       s.DiskGB,
				Unprivileged: s.Unprivileged,
				Nesting:      s.Nesting,
				HostManaged:  s.HostManagedNet,
				DHCP:         s.DHCP,
				IPCIDR:       s.IPCIDR,
				Gateway:      s.Gateway,
				DNSServers:   s.DNS,
				SSHKeys:      s.SSHKeys,
			}
			continue
		}
		// Like the CT branch: an explicit MemoryMB wins, so an adopted VM with RAM
		// not aligned to a whole GB (e.g. 2560 MB) keeps its exact size.
		mem := s.MemoryGB * 1024
		if s.MemoryMB != 0 {
			mem = s.MemoryMB
		}
		vms[s.Name] = tfVM{
			Node:       s.Node,
			VMID:       s.VMID,
			TemplateID: s.TemplateID,
			Storage:    s.Storage,
			Bridge:     s.Bridge,
			Cores:      s.Cores,
			MemoryMB:   mem,
			DiskGB:     s.DiskGB,
			CPUType:    s.CPUType,
			DHCP:       s.DHCP,
			IPCIDR:     s.IPCIDR,
			Gateway:    s.Gateway,
			DNSServers: s.DNS,
			Username:   s.Username,
			SSHKeys:    s.SSHKeys,
		}
	}
	return writeJSON(filepath.Join(r.Dir, "terraform.tfvars.json"),
		map[string]any{"vms": vms, "cts": cts})
}

// ExistingPasswords reads previously stored passwords (both VM cloud-init and LXC
// root) from the gitignored secrets file, so re-syncing the workspace doesn't drop
// them. The two sections are merged into one name->password map; names are
// cluster-unique, so there is no collision.
func (r *Runner) ExistingPasswords() (map[string]string, error) {
	path := filepath.Join(r.Dir, "secrets.auto.tfvars.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var s struct {
		VMPasswords map[string]string `json:"vm_passwords"`
		CTPasswords map[string]string `json:"ct_passwords"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for name, pw := range s.VMPasswords {
		merged[name] = pw
	}
	for name, pw := range s.CTPasswords {
		merged[name] = pw
	}
	return merged, nil
}

func (r *Runner) writeSecrets(vmPasswords, ctPasswords map[string]string) error {
	path := filepath.Join(r.Dir, "secrets.auto.tfvars.json")
	if len(vmPasswords) == 0 && len(ctPasswords) == 0 {
		_ = os.Remove(path)
		return nil
	}
	return writeJSONPerm(path, map[string]any{
		"vm_passwords": vmPasswords,
		"ct_passwords": ctPasswords,
	}, 0o600)
}

// env returns the process environment plus the Proxmox provider credentials.
func (r *Runner) env() []string {
	env := os.Environ()
	env = append(env,
		"PROXMOX_VE_ENDPOINT="+r.Cfg.ProxmoxURL,
		fmt.Sprintf("PROXMOX_VE_API_TOKEN=%s=%s", r.Cfg.TokenID, r.Cfg.TokenSecret),
	)
	if r.Cfg.Insecure {
		env = append(env, "PROXMOX_VE_INSECURE=true")
	}
	return env
}

// run executes terraform. When Verbose is 0 the output is captured and only
// printed if the command fails; otherwise it is streamed live.
func (r *Runner) run(args ...string) error {
	cmd := r.command(args...)
	cmd.Dir = r.Dir
	cmd.Env = r.env()
	if r.Out != nil {
		cmd.Stdout, cmd.Stderr = r.Out, r.Out
		return cmd.Run()
	}
	if r.Verbose > 0 {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Stderr.Write(out)
	}
	return err
}

// stream always streams output live, regardless of verbosity (for plan/dry-run).
func (r *Runner) stream(args ...string) error {
	cmd := r.command(args...)
	cmd.Dir = r.Dir
	cmd.Env = r.env()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Init runs `terraform init` (idempotent).
func (r *Runner) Init() error {
	return r.run("init", "-input=false", "-upgrade")
}

// Apply runs `terraform apply -auto-approve`, optionally targeting a single guest
// (VM or container). A nil target applies the whole workspace.
func (r *Runner) Apply(target *state.VMSpec) error {
	args := []string{"apply", "-input=false", "-auto-approve"}
	if target != nil {
		args = append(args, "-target="+resourceAddr(target))
	}
	return r.run(args...)
}

// Plan runs `terraform plan` and always streams the output (used for --dry-run).
func (r *Runner) Plan() error {
	return r.stream("plan", "-input=false")
}

// Refresh updates terraform state/outputs from the real infrastructure without
// changing anything (used to (re)discover IP addresses).
func (r *Runner) Refresh() error {
	return r.run("apply", "-refresh-only", "-input=false", "-auto-approve")
}

// IPAddresses returns the discovered IPv4 addresses per VM from terraform output.
// The provider reports a list of lists (one per network interface); they are
// flattened here. Returns an empty map when there is no state yet.
func (r *Runner) IPAddresses() map[string][]string {
	cmd := exec.Command("terraform", "output", "-json", "ip_addresses")
	cmd.Dir = r.Dir
	cmd.Env = r.env()
	out, err := cmd.Output()
	if err != nil {
		return map[string][]string{}
	}
	var nested map[string][][]string
	if err := json.Unmarshal(out, &nested); err != nil {
		return map[string][]string{}
	}
	flat := make(map[string][]string, len(nested))
	for name, ifaces := range nested {
		for _, addrs := range ifaces {
			flat[name] = append(flat[name], addrs...)
		}
	}
	return flat
}

// Destroy removes a single guest (VM or container) by destroying its targeted
// resource.
func (r *Runner) Destroy(target *state.VMSpec) error {
	return r.run("destroy", "-input=false", "-auto-approve",
		"-target="+resourceAddr(target))
}

// Import brings an existing Proxmox guest under Terraform management by
// importing it into state at the resource address hlab would normally create it
// under. The bpg import ID for both resource types is "<node>/<vmid>".
func (r *Runner) Import(target *state.VMSpec) error {
	return r.run("import", "-input=false", resourceAddr(target),
		fmt.Sprintf("%s/%d", target.Node, target.VMID))
}

// StateRm removes a resource from state without touching the real
// infrastructure — used to roll back a failed adoption; the guest must stay
// exactly as it was found.
func (r *Runner) StateRm(target *state.VMSpec) error {
	return r.run("state", "rm", resourceAddr(target))
}

// PatchResourceAttrs rewrites arbitrary attributes of a guest's resource
// instance directly in the Terraform state (state pull → edit → push),
// preserving every other attribute. Used both to re-anchor a container's node
// after an out-of-band API migration (SetResourceNode) and to fill in the
// config-only attributes an import leaves null (adopt — see AdoptedStateAttrs).
//
// This deliberately avoids `state rm` + `import` for anything beyond adopt's
// initial import: importing a bpg guest comes back with the config-only
// attributes null in state (notably vm_id and the timeout_* fields). The
// provider derives the delete/update operation's context deadline from
// timeout_delete/timeout_update, so a null there yields a zero-length deadline
// and the very next status read fails instantly with "context deadline
// exceeded" — leaving the guest un-updatable and un-deletable via Terraform.
// Adopt calls Import exactly once and then patches those attributes back in
// with PatchResourceAttrs(AdoptedStateAttrs(...)), keeping the create-time
// state consistent without a second import.
func (r *Runner) PatchResourceAttrs(target *state.VMSpec, attrs map[string]any) error {
	pull := r.command("state", "pull")
	pull.Dir = r.Dir
	pull.Env = r.env()
	data, err := pull.Output()
	if err != nil {
		return fmt.Errorf("state pull: %w", err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("parsing state: %w", err)
	}
	// A push is only accepted with a serial greater than the current one.
	if s, ok := st["serial"].(float64); ok {
		st["serial"] = s + 1
	}
	resType := "proxmox_virtual_environment_vm"
	resName := "vm"
	if target.IsLXC() {
		resType, resName = "proxmox_virtual_environment_container", "ct"
	}
	found := false
	if resources, ok := st["resources"].([]any); ok {
		for _, ri := range resources {
			res, _ := ri.(map[string]any)
			if res["type"] != resType || res["name"] != resName {
				continue
			}
			insts, _ := res["instances"].([]any)
			for _, ii := range insts {
				inst, _ := ii.(map[string]any)
				if fmt.Sprint(inst["index_key"]) != target.Name {
					continue
				}
				instAttrs, _ := inst["attributes"].(map[string]any)
				if instAttrs == nil {
					continue
				}
				for k, v := range attrs {
					instAttrs[k] = v
				}
				found = true
			}
		}
	}
	if !found {
		return fmt.Errorf("resource for %q not found in state", target.Name)
	}
	patched, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// Write the patched state to a temp file OUTSIDE the git-tracked workspace
	// (~/.hlab/terraform): a full `state pull` dump contains the cloud-init
	// password in plaintext, so a crash between here and the deferred remove must
	// not leave it where the next `Store.Commit` (git add -A) would commit it.
	// os.CreateTemp creates it 0600.
	tmp, err := os.CreateTemp("", "hlab-state-patch-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, werr := tmp.Write(patched)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return r.run("state", "push", tmpName)
}

// SetResourceNode rewrites a guest's node_name — the one-attribute case of
// PatchResourceAttrs, used to re-anchor a container after an out-of-band API
// migration.
func (r *Runner) SetResourceNode(target *state.VMSpec, node string) error {
	return r.PatchResourceAttrs(target, map[string]any{"node_name": node})
}

// AdoptedStateAttrs returns the config-only state attributes that Import leaves
// null, keyed for PatchResourceAttrs. These are exactly the create-time-only
// attributes bpg cannot recover from a live import (see the PatchResourceAttrs
// doc comment) — adopt fills them in immediately after Import so the guest
// stays fully manageable (update/delete/migrate) without a second import. The
// timeout values match the provider's documented defaults.
func AdoptedStateAttrs(target *state.VMSpec) map[string]any {
	if target.IsLXC() {
		return map[string]any{
			"vm_id":          target.VMID,
			"timeout_create": 1800,
			"timeout_clone":  1800,
			"timeout_update": 1800,
			"timeout_delete": 60,
			"timeout_start":  300,
		}
	}
	return map[string]any{
		"vm_id":               target.VMID,
		"timeout_clone":       1800,
		"timeout_create":      1800,
		"timeout_migrate":     1800,
		"timeout_move_disk":   1800,
		"timeout_reboot":      1800,
		"timeout_shutdown_vm": 1800,
		"timeout_start_vm":    1800,
		"timeout_stop_vm":     300,
		// main.tf sets migrate = true; matching it here avoids a spurious diff.
		"migrate": true,
	}
}

// streamPlanLog decodes a `terraform plan -json` stream and forwards each
// message's human-readable text (@message) to out, one line per message. A
// nil out is a no-op. Non-JSON lines are tolerated (skipped) rather than
// failing the scan — terraform's -json output is one JSON object per line, but
// this must not choke on stray output. Shared by PlanDetailed and DriftReport,
// which both classify the same stream themselves and only need this for the
// live log forwarding (the TUI log panel / -v).
func streamPlanLog(r io.Reader, out io.Writer) {
	if out == nil {
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg struct {
			Message string `json:"@message"`
		}
		if jerr := json.Unmarshal(sc.Bytes(), &msg); jerr != nil {
			continue
		}
		if msg.Message != "" {
			fmt.Fprintln(out, msg.Message)
		}
	}
}

// PlanDetailed runs a targeted `terraform plan -detailed-exitcode -json` and
// classifies the result. Exit 0 means no changes; exit 2 means changes are
// pending — parsed into a human-readable summary and whether any of them force
// a replace; exit 1 means the plan itself failed (diagnostics collected into
// err). Each plan message's @message is forwarded to r.Out when set (the TUI
// log panel), like other long-running steps.
//
// Used by adopt to verify, right after importing a guest, that the declared
// spec doesn't drift from reality in a way that would force a replace on the
// next real apply — that would mean the "adoption" destroys and recreates the
// guest.
func (r *Runner) PlanDetailed(target *state.VMSpec) (changes, replace bool, summary string, err error) {
	cmd := r.command("plan", "-input=false", "-no-color", "-json",
		"-detailed-exitcode", "-target="+resourceAddr(target))
	cmd.Dir = r.Dir
	cmd.Env = r.env()
	stdout, runErr := cmd.Output()
	streamPlanLog(bytes.NewReader(stdout), r.Out)

	var lines []string
	var diags []string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg struct {
			Message string `json:"@message"`
			Type    string `json:"type"`
			Change  struct {
				Resource struct {
					Addr string `json:"addr"`
				} `json:"resource"`
				Action string `json:"action"`
			} `json:"change"`
			Diagnostic struct {
				Summary string `json:"summary"`
				Detail  string `json:"detail"`
			} `json:"diagnostic"`
		}
		if jerr := json.Unmarshal(sc.Bytes(), &msg); jerr != nil {
			continue // tolerate a non-JSON line rather than failing the whole plan
		}
		switch msg.Type {
		case "planned_change":
			lines = append(lines, fmt.Sprintf("%s: %s", msg.Change.Resource.Addr, msg.Change.Action))
			switch msg.Change.Action {
			case "replace", "create", "delete":
				// create/delete on the targeted address means the import didn't
				// land where expected — treat it the same as a replace.
				replace = true
			}
		case "change_summary":
			if msg.Message != "" {
				lines = append(lines, msg.Message)
			}
		case "diagnostic":
			d := msg.Diagnostic.Summary
			if msg.Diagnostic.Detail != "" {
				d += ": " + msg.Diagnostic.Detail
			}
			if d != "" {
				diags = append(diags, d)
			}
		}
	}
	summary = strings.Join(lines, "\n")

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return false, false, summary, runErr // exec itself failed (binary missing, ctx cancelled, …)
		}
	}

	switch exitCode {
	case 0:
		return false, false, "", nil
	case 2:
		return true, replace, summary, nil
	default: // 1, or an unexpected code
		if len(diags) > 0 {
			return false, false, summary, fmt.Errorf("%s", strings.Join(diags, "; "))
		}
		return false, false, summary, fmt.Errorf("terraform plan failed (exit %d)", exitCode)
	}
}

// DriftChange is a single guest's classified drift, as reported by
// DriftReport. Action mirrors the terraform plan action that would apply:
// "update" is an in-place change, "replace" would destroy and recreate the
// guest, "create"/"delete" mean the guest is missing from (or unexpectedly
// present outside) state entirely.
type DriftChange struct {
	Name   string // resource index_key (guest name)
	IsLXC  bool
	Action string   // "update" | "replace" | "create" | "delete"
	Attrs  []string // meaningful changed attribute paths, e.g. "cpu.cores"
}

// DriftReport runs a read-only `terraform plan` (optionally scoped to
// targets) and classifies each changed guest into a DriftChange, filtering
// out the benign, hlab/provider-bookkeeping and purely-computed noise that a
// raw plan reports on every guest today (migrate, started, computed
// *_addresses, …) — see driftIgnore/driftPaths. No targets plans the whole
// workspace (used by DetectDrift for the fleet-wide `hlab plan`); one or more
// targets restricts it to those guests. Nothing is applied and no .tf files
// change — the plan is written to a scratch plan file that is removed before
// returning.
func (r *Runner) DriftReport(targets ...*state.VMSpec) ([]DriftChange, error) {
	// Keep the binary plan out of the git-tracked workspace: it embeds resource
	// attribute values (secrets included), so a crash must not strand it where the
	// next `Store.Commit` would pick it up. os.CreateTemp makes it 0600; terraform
	// overwrites it via -out.
	pf, err := os.CreateTemp("", "hlab-drift-*.tfplan")
	if err != nil {
		return nil, err
	}
	planFile := pf.Name()
	_ = pf.Close()
	defer os.Remove(planFile)

	args := []string{"plan", "-input=false", "-no-color", "-json", "-out=" + planFile}
	for _, t := range targets {
		args = append(args, "-target="+resourceAddr(t))
	}
	cmd := r.command(args...)
	cmd.Dir = r.Dir
	cmd.Env = r.env()
	stdout, runErr := cmd.Output()
	streamPlanLog(bytes.NewReader(stdout), r.Out)

	// Exit code semantics match PlanDetailed: 0 (no changes) and 2 (changes
	// pending) are both a successful plan; 1 (or a non-ExitError, e.g. the
	// binary is missing or the context was cancelled) is a real failure.
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, runErr
		}
	}
	if exitCode != 0 && exitCode != 2 {
		if diags := planDiagnostics(stdout); len(diags) > 0 {
			return nil, fmt.Errorf("%s", strings.Join(diags, "; "))
		}
		return nil, fmt.Errorf("terraform plan failed (exit %d)", exitCode)
	}

	show := r.command("show", "-json", planFile)
	show.Dir = r.Dir
	show.Env = r.env()
	out, err := show.Output()
	if err != nil {
		return nil, fmt.Errorf("terraform show: %w", err)
	}

	var doc struct {
		ResourceChanges []struct {
			Type, Name, Index string
			Change            struct {
				Actions      []string `json:"actions"`
				Before       any      `json:"before"`
				After        any      `json:"after"`
				AfterUnknown any      `json:"after_unknown"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}

	hasAction := func(actions []string, a string) bool {
		for _, x := range actions {
			if x == a {
				return true
			}
		}
		return false
	}

	var changes []DriftChange
	for _, rc := range doc.ResourceChanges {
		actions := rc.Change.Actions
		if len(actions) == 1 && actions[0] == "no-op" {
			continue
		}
		isLXC := strings.HasSuffix(rc.Type, "_container")

		if hasAction(actions, "create") || hasAction(actions, "delete") || hasAction(actions, "replace") {
			action := "create"
			switch {
			case hasAction(actions, "replace") || (hasAction(actions, "create") && hasAction(actions, "delete")):
				action = "replace"
			case hasAction(actions, "delete"):
				action = "delete"
			}
			// Attrs are informational here (the action itself already says
			// enough), but still worth computing for the detail view.
			attrs := driftPaths(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, "")
			changes = append(changes, DriftChange{Name: rc.Index, IsLXC: isLXC, Action: action, Attrs: attrs})
			continue
		}

		if len(actions) == 1 && actions[0] == "update" {
			attrs := driftPaths(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, "")
			if len(attrs) == 0 {
				continue // nothing but ignored/computed noise — not real drift
			}
			changes = append(changes, DriftChange{Name: rc.Index, IsLXC: isLXC, Action: "update", Attrs: attrs})
		}
	}
	return changes, nil
}

// planDiagnostics extracts diagnostic summaries from a terraform plan -json
// stream, used to surface a clearer error than a bare exit code when
// DriftReport's plan fails outright.
func planDiagnostics(stdout []byte) []string {
	var diags []string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg struct {
			Type       string `json:"type"`
			Diagnostic struct {
				Summary string `json:"summary"`
				Detail  string `json:"detail"`
			} `json:"diagnostic"`
		}
		if jerr := json.Unmarshal(sc.Bytes(), &msg); jerr != nil {
			continue
		}
		if msg.Type != "diagnostic" {
			continue
		}
		d := msg.Diagnostic.Summary
		if msg.Diagnostic.Detail != "" {
			d += ": " + msg.Diagnostic.Detail
		}
		if d != "" {
			diags = append(diags, d)
		}
	}
	return diags
}

// driftIgnoreSet is the curated set of attribute names — matched against the
// full dotted path or any single dotted segment — that a raw `terraform plan`
// reports as changed on nearly every guest today but which are hlab/provider
// bookkeeping or purely computed noise, not real drift: toggles hlab itself
// manages across applies (migrate/started/on_boot/…), state-patch-only
// timeout_* fields (see AdoptedStateAttrs), and outputs that read as "known
// after apply" on a fresh plan (ipv4_addresses, …). Verified against the live
// fleet: this filter reduces today's benign per-guest changes to 0 drift,
// while a real change (e.g. cpu.cores) still surfaces.
var driftIgnoreSet = map[string]bool{
	"migrate":                 true,
	"started":                 true,
	"start_on_boot":           true,
	"reboot":                  true,
	"reboot_after_update":     true,
	"stop_on_destroy":         true,
	"on_boot":                 true,
	"protection":              true,
	"tags":                    true,
	"description":             true,
	"console":                 true,
	"template":                true,
	"hook_script_file_id":     true,
	"pool_id":                 true,
	"ipv4_addresses":          true,
	"ipv6_addresses":          true,
	"network_interface_names": true,
	"mac_addresses":           true,
}

// driftIgnoreFeatures are LXC features.* leaves that are provider/container
// defaults hlab never declares. features.nesting is deliberately absent: it's
// the one feature hlab actually manages (VMSpec.Nesting) and must remain
// visible as real drift.
var driftIgnoreFeatures = map[string]bool{
	"features.keyctl": true,
	"features.fuse":   true,
	"features.mknod":  true,
	"features.mount":  true,
}

// driftIgnore reports whether path is bookkeeping/computed noise that must
// never be surfaced as drift: a full-path or per-segment match against
// driftIgnoreSet, a timeout_* segment prefix, an ip_config…ipv6 path suffix
// (only the IPv4 side of initialization.ip_config is real drift), or one of
// the ignored LXC features.* leaves.
func driftIgnore(path string) bool {
	if path == "" {
		return false
	}
	if driftIgnoreFeatures[path] {
		return true
	}
	if strings.HasSuffix(path, ".ipv6") {
		return true
	}
	for _, seg := range strings.Split(path, ".") {
		if driftIgnoreSet[seg] {
			return true
		}
		if strings.HasPrefix(seg, "timeout_") {
			return true
		}
	}
	return false
}

// driftPaths recursively diffs before vs after — both decoded from
// terraform's plan JSON, so map[string]any / []any / scalars/nil — returning
// the leaf attribute paths that changed for real. unknown mirrors the shape
// of the corresponding after_unknown subtree.
//
// A leaf is pruned (not reported) when driftIgnore matches its path, or when
// unknown is the literal boolean true — meaning that leaf is computed
// ("known after apply") rather than actually different. Nested computed
// blocks (cpu/memory/disk/initialization/…) report after_unknown as a
// map/slice, not a bare true, so they are NOT pruned by that rule and get
// recursed into instead — that's exactly where real drift (e.g. cpu.cores)
// shows up.
//
// List/slice fields are recursed index-by-index while keeping path stable
// (not appending "[i]"), so a nested field reads as
// "initialization.ip_config.ipv4.address" rather than
// "initialization.ip_config.0.ipv4.address" — a length mismatch is reported
// once at the list's own path instead. Duplicate paths (a stable path can
// repeat across multiple list indices) are deduped before returning.
func driftPaths(before, after, unknown any, path string) []string {
	if driftIgnore(path) {
		return nil
	}
	if b, ok := unknown.(bool); ok && b {
		return nil
	}

	bm, bIsMap := before.(map[string]any)
	am, aIsMap := after.(map[string]any)
	if bIsMap || aIsMap {
		um, _ := unknown.(map[string]any)
		keys := map[string]bool{}
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		var out []string
		for k := range keys {
			var uv any
			if um != nil {
				uv = um[k]
			}
			out = append(out, driftPaths(bm[k], am[k], uv, join(path, k))...)
		}
		return dedup(out)
	}

	bs, bIsSlice := before.([]any)
	as, aIsSlice := after.([]any)
	if bIsSlice || aIsSlice {
		if len(bs) != len(as) {
			// A length mismatch where one side is empty is a state-representation
			// gap, not real divergence: for the nested blocks hlab models
			// (cpu/memory/disk/network/initialization) reality always HAS the
			// block, so an empty `before` means the provider simply didn't read
			// it back into state (common for a guest that was imported/adopted
			// but never applied — its cpu block stays [] with units:null). The
			// config-side block is then reported as "added", which isn't drift.
			// Only a mismatch between two NON-empty lists is a real structural
			// change (e.g. a second disk actually appeared).
			if len(bs) == 0 || len(as) == 0 {
				return nil
			}
			return []string{path}
		}
		us, _ := unknown.([]any)
		var out []string
		for i := range bs {
			var uv any
			if us != nil && i < len(us) {
				uv = us[i]
			}
			out = append(out, driftPaths(bs[i], as[i], uv, path)...)
		}
		return dedup(out)
	}

	if equalJSON(before, after) {
		return nil
	}
	return []string{path}
}

// join builds a dotted attribute path, e.g. join("", "cpu") == "cpu" and
// join("cpu", "cores") == "cpu.cores".
func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// dedup removes duplicate paths while preserving order — a stable path (see
// driftPaths' list handling) can repeat across multiple list indices.
func dedup(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	seen := make(map[string]bool, len(paths))
	out := paths[:0]
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// equalJSON compares two decoded-JSON values (map[string]any/[]any/scalars/
// nil) via their canonical json.Marshal encoding, so e.g. numeric types and
// map key ordering don't cause a false positive.
func equalJSON(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return bytes.Equal(ab, bb)
}

func writeJSON(path string, v any) error { return writeJSONPerm(path, v, 0o644) }

func writeJSONPerm(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}
