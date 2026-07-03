package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/sshutil"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/wizard"
)

// runDoneMsg ends a streaming operation. A non-nil err is shown in the footer;
// otherwise status is. thenProvision, when set, opens the provision form for the
// just-created VM (the create → provision chain).
type runDoneMsg struct {
	status        string
	err           error
	thenProvision *state.VMSpec
}

// logLineMsg is one line of streamed tool output, appended to the log panel.
type logLineMsg string

// driftDoneMsg ends a whole-fleet drift detection run (key P).
type driftDoneMsg struct {
	statuses []engine.DriftStatus
	err      error
}

// snapsLoadedMsg carries a VM's snapshots for the browser (modeSnaps).
type snapsLoadedMsg struct {
	vm    *state.VMSpec
	snaps []proxmox.Snapshot
	err   error
}

// runOpMsg starts a run-modal operation from a deferred trigger (e.g. after a
// confirmation), so a confirmed action can enter modeRun with a progress bar.
type runOpMsg struct {
	title string
	cmd   tea.Cmd
}

// wrapRunOp defers a run-modal operation into a command, so it can be handed to
// askConfirm and only fire (with the progress bar) once the user confirms.
func wrapRunOp(title string, cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg { return runOpMsg{title: title, cmd: cmd} }
}

// loadSnaps fetches a VM's snapshots off the UI thread for the browser.
func (m Model) loadSnaps(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		snaps, err := eng.Snapshots(vm.Name)
		return snapsLoadedMsg{vm: vm, snaps: snaps, err: err}
	}
}

// streamSnapshotCreate / Rollback / Delete run a snapshot operation and report
// completion. They are backed by the Proxmox task API (the engine waits for the
// task), so there is no streamed text — the run modal shows the animated bar.
func (m Model) streamSnapshotCreate(name, snap, desc string, withRAM bool) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.Snapshot(name, snap, desc, withRAM); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("snapshot %q created for %s", snap, name)}
	}
}

func (m Model) streamSnapshotRollback(name, snap string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.RollbackSnapshot(name, snap); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("%s rolled back to %q", name, snap)}
	}
}

func (m Model) streamSnapshotDelete(name, snap string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.DeleteSnapshot(name, snap); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("snapshot %q deleted for %s", snap, name)}
	}
}

// suspend releases the terminal so an external interactive process (ssh) can own
// it, runs do, then restores the dashboard.
func suspend(do func() error) error {
	if program == nil {
		return do()
	}
	_ = program.ReleaseTerminal()
	defer func() { _ = program.RestoreTerminal() }()
	return do()
}

// pipeInto runs a blocking operation while piping everything it writes to the
// given writer, line by line, into the dashboard log via program.Send.
func pipeInto(op func(w io.Writer) error) error {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if program != nil {
				program.Send(logLineMsg(sc.Text()))
			}
		}
		// sc.Err() is deliberately ignored: a single line longer than the 1 MB cap
		// ends Scan early with bufio.ErrTooLong, which we treat like EOF — stop
		// forwarding lines to the log but keep draining the pipe so os/exec's stdout
		// copy never blocks on a full pipe (that would hang op(pw)/cmd.Wait() forever
		// with no recovery). On the normal EOF path this returns immediately, and the
		// operation still completes.
		_ = sc.Err()
		_, _ = io.Copy(io.Discard, pr)
		close(done)
	}()
	err := op(pw)
	_ = pw.Close()
	<-done
	return err
}

// The stream* actions below capture eng := m.eng and drive shared engine/runner
// state (Runner via SetOut/SetCtx, plus the AnsibleOut field) from inside their
// worker goroutine. This is safe ONLY because the TUI is modal: an op runs in
// modeRun, and Setup (which reloadEngine reloads the same runner/PM for) can only
// be opened from modeDash — the two are never in flight at once. If that invariant
// is ever relaxed (e.g. a background op that keeps running after returning to
// modeDash), these would need the same snapshot-before-spawn treatment as
// refresh()/resolveGuestIPs.

// streamCreate runs terraform apply for a new VM, streaming output to the log,
// then chains into the provision form for that VM.
func (m Model) streamCreate(res *wizard.Result) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		var ip string
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			var e error
			ip, e = eng.Create(res)
			return e
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{
			status:        fmt.Sprintf("created %s (%s)", res.VM.Name, ip),
			thenProvision: res.VM,
		}
	}
}

// streamProvision runs ansible for the VM's selected software/dotfiles.
func (m Model) streamProvision(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		err := pipeInto(func(w io.Writer) error {
			eng.AnsibleOut = w
			defer func() { eng.AnsibleOut = nil }()
			return eng.Provision(vm)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("provisioned %s", vm.Name)}
	}
}

// streamDestroy runs terraform destroy for a VM.
func (m Model) streamDestroy(name string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			return eng.Destroy(name)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("destroyed %s", name)}
	}
}

// streamMigrate runs terraform apply to migrate a VM to another node, streaming
// output to the log. The bpg provider migrates (migrate=true) rather than
// recreating, so the disk and VM id are preserved.
func (m Model) streamMigrate(name, toNode string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			return eng.Migrate(name, toNode)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("migrated %s to %s", name, toNode)}
	}
}

// streamReconfigure runs terraform apply to change a VM's cores/memory/disk,
// streaming output to the log. The bpg provider updates the VM in place.
func (m Model) streamReconfigure(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			return eng.Reconfigure(vm)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("reconfigured %s", vm.Name)}
	}
}

// streamAdopt imports a discovered guest into Terraform state (Engine.Adopt),
// streaming terraform output to the log. The live guest is never modified —
// any failure rolls back hlab's own artifacts and leaves the guest untouched.
// A non-empty drift summary means the guest matched the declaration except for
// in-place changes the next apply will make; it's appended to the log and
// surfaced in the result status. The refresh after this run moves the guest
// from DISCOVERED to MANAGED.
func (m Model) streamAdopt(spec *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		var drift string
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			var e error
			drift, e = eng.Adopt(spec)
			return e
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		if drift != "" {
			if program != nil {
				program.Send(logLineMsg("in-place drift — the next apply will reconcile:"))
				for line := range strings.SplitSeq(drift, "\n") {
					program.Send(logLineMsg(line))
				}
			}
			return runDoneMsg{status: fmt.Sprintf("adopted %s (in-place drift — see log)", spec.Name)}
		}
		return runDoneMsg{status: fmt.Sprintf("adopted %s", spec.Name)}
	}
}

// streamPlan runs a whole-fleet, read-only `terraform plan` (Engine.DetectDrift)
// and reports every managed guest's drift classification. Triggered on-demand
// (key P) — never by the periodic refresh.
func (m Model) streamPlan() tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		var st []engine.DriftStatus
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			var e error
			st, e = eng.DetectDrift()
			return e
		})
		return driftDoneMsg{statuses: st, err: err}
	}
}

// streamUpdate re-provisions a VM idempotently (Engine.Update): reruns Ansible
// with its saved software/dotfiles selection, no reprompt. upgrade additionally
// applies apt/mise/runtime upgrades and re-runs the CLI installers.
func (m Model) streamUpdate(vm *state.VMSpec, upgrade bool) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		err := pipeInto(func(w io.Writer) error {
			eng.AnsibleOut = w
			defer func() { eng.AnsibleOut = nil }()
			return eng.Update(vm, upgrade)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		word := "updated"
		if upgrade {
			word = "upgraded"
		}
		return runDoneMsg{status: fmt.Sprintf("%s %s", word, vm.Name)}
	}
}

// streamInjectKey installs an SSH public key on the selected managed guest: it
// resolves the guest's IP the way the CLI does, appends the key to the live
// guest's authorized_keys over SSH (sshutil.AppendAuthorizedKey — the same helper
// `hlab {vm,ct} add-ssh-key` uses), then records it in the declaration and
// reconciles Terraform (Engine.AddSSHKey). For a VM the AddSSHKey step runs a
// targeted terraform apply, so its output is streamed to the log like the other
// mutating ops.
func (m Model) streamInjectKey(vm *state.VMSpec, pub, consolePassword string) tea.Cmd {
	eng := m.eng
	// A keyless LXC has no SSH access, so the first key must go in over the Proxmox
	// console (the SSH path can't authenticate). Once a key exists, the normal SSH
	// path is used for any subsequent key. consolePassword is the operator-entered
	// root password when the inject form asked for one (no password stored); empty
	// otherwise, in which case the stored password is used.
	keylessLXC := vm.IsLXC() && len(vm.SSHKeys) == 0
	// A keyless VM has no SSH access either, but no console: its first key goes in
	// through the QEMU guest agent (runs as root inside the VM, no login needed).
	keylessVM := !vm.IsLXC() && len(vm.SSHKeys) == 0
	return func() tea.Msg {
		if keylessVM {
			err := pipeInto(func(w io.Writer) error {
				eng.Runner.SetOut(w)
				defer func() { eng.Runner.SetOut(nil) }()
				fmt.Fprintf(w, "no SSH access to %s — injecting the key via the QEMU guest agent…\n", vm.Name)
				return eng.InjectSSHKeyViaAgent(vm, pub)
			})
			if err != nil {
				return runDoneMsg{err: err}
			}
			return runDoneMsg{status: fmt.Sprintf("added SSH key to %s (via guest agent)", vm.Name)}
		}
		if keylessLXC {
			err := pipeInto(func(w io.Writer) error {
				eng.Runner.SetOut(w)
				defer func() { eng.Runner.SetOut(nil) }()
				fmt.Fprintf(w, "no SSH access to %s — injecting the key via the Proxmox console…\n", vm.Name)
				if consolePassword != "" {
					return eng.InjectSSHKeyViaConsoleWithPassword(vm, pub, consolePassword)
				}
				return eng.InjectSSHKeyViaConsole(vm, pub)
			})
			if err != nil {
				return runDoneMsg{err: err}
			}
			return runDoneMsg{status: fmt.Sprintf("added SSH key to %s (via console)", vm.Name)}
		}
		ip := eng.ResolveIP(vm)
		if ip == "" {
			return runDoneMsg{err: fmt.Errorf("no IP address known for %q yet", vm.Name)}
		}
		err := pipeInto(func(w io.Writer) error {
			eng.Runner.SetOut(w)
			defer func() { eng.Runner.SetOut(nil) }()
			fmt.Fprintf(w, "installing SSH key on %s (%s@%s)…\n", vm.Name, vm.Username, ip)
			if e := sshutil.AppendAuthorizedKey(vm.Username, ip, pub); e != nil {
				return e
			}
			fmt.Fprintln(w, "recording the key in the declaration…")
			return eng.AddSSHKey(vm, pub)
		})
		if err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("added SSH key to %s", vm.Name)}
	}
}

// powerOn starts a stopped VM via the Proxmox API (no Terraform involved).
func (m Model) powerOn(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.Start(vm.Name); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("started %s", vm.Name)}
	}
}

// powerOff requests a graceful shutdown of a running VM. Proxmox queues the
// task, so the status reflects the change only once the guest powers off.
func (m Model) powerOff(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.Stop(vm.Name, false); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("shutting down %s", vm.Name)}
	}
}

// rebootVM requests a graceful guest reboot of a running VM via the Proxmox API.
func (m Model) rebootVM(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.Reboot(vm.Name); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("rebooting %s", vm.Name)}
	}
}

// startGuest / stopGuest / rebootGuest are the power actions for a discovered
// (unmanaged) guest. stop/reboot are gated behind a confirmation in the dash.
func (m Model) startGuest(g proxmox.Guest) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.StartGuest(g.Node, g.Type, g.VMID); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("started %s", g.Name)}
	}
}

func (m Model) stopGuest(g proxmox.Guest) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.StopGuest(g.Node, g.Type, g.VMID, false); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("shutting down %s", g.Name)}
	}
}

func (m Model) rebootGuest(g proxmox.Guest) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		if err := eng.RebootGuest(g.Node, g.Type, g.VMID); err != nil {
			return runDoneMsg{err: err}
		}
		return runDoneMsg{status: fmt.Sprintf("rebooting %s", g.Name)}
	}
}

// startSSH opens an interactive ssh session to the selected VM (suspend/resume),
// returning to the dashboard when it closes.
func (m Model) startSSH(vm *state.VMSpec) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ip := eng.ResolveIP(vm)
		if ip == "" {
			return runDoneMsg{err: fmt.Errorf("no IP address known for %q yet", vm.Name)}
		}
		_ = suspend(func() error {
			c := exec.Command("ssh", fmt.Sprintf("%s@%s", vm.Username, ip))
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run() // a non-zero exit on logout is not an error to surface
		})
		return runDoneMsg{status: fmt.Sprintf("ssh %s closed", vm.Name)}
	}
}
