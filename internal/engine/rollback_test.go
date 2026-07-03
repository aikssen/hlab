package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
	"github.com/aikssen/hlab/internal/wizard"
)

// fakeRunner is a test double for the engine.Runner interface: it records which
// mutating calls happened and lets a test inject failures, so the create/adopt
// rollback branches can be exercised without shelling out to terraform.
type fakeRunner struct {
	applyErr     error
	initErr      error
	importErr    error
	patchErr     error
	stateRmErr   error
	planChanges  bool
	planReplace  bool
	planSummary  string
	planErr      error
	driftChanges []terraform.DriftChange // returned by DriftReport
	driftErr     error                   // returned by DriftReport
	ips          map[string][]string
	passwords    map[string]string // returned by ExistingPasswords

	applyCalls   int
	importCalls  int
	destroyCalls int
	stateRmCalls int
	setNodeCalls int
	setNodeTo    string // captured target from the last SetResourceNode
	detached     bool
}

func (f *fakeRunner) Sync([]*state.VMSpec, map[string]string) error { return nil }
func (f *fakeRunner) Init() error                                   { return f.initErr }
func (f *fakeRunner) Apply(*state.VMSpec) error                     { f.applyCalls++; return f.applyErr }
func (f *fakeRunner) Destroy(*state.VMSpec) error                   { f.destroyCalls++; return nil }
func (f *fakeRunner) Plan() error                                   { return nil }
func (f *fakeRunner) Refresh() error                                { return nil }
func (f *fakeRunner) IPAddresses() map[string][]string              { return f.ips }
func (f *fakeRunner) ExistingPasswords() (map[string]string, error) {
	if f.passwords != nil {
		return f.passwords, nil
	}
	return map[string]string{}, nil
}
func (f *fakeRunner) Import(*state.VMSpec) error                             { f.importCalls++; return f.importErr }
func (f *fakeRunner) PatchResourceAttrs(*state.VMSpec, map[string]any) error { return f.patchErr }
func (f *fakeRunner) SetResourceNode(_ *state.VMSpec, node string) error {
	f.setNodeCalls++
	f.setNodeTo = node
	return nil
}
func (f *fakeRunner) StateRm(*state.VMSpec) error { f.stateRmCalls++; return f.stateRmErr }
func (f *fakeRunner) PlanDetailed(*state.VMSpec) (bool, bool, string, error) {
	return f.planChanges, f.planReplace, f.planSummary, f.planErr
}
func (f *fakeRunner) DriftReport(...*state.VMSpec) ([]terraform.DriftChange, error) {
	return f.driftChanges, f.driftErr
}
func (f *fakeRunner) SetOut(io.Writer)       {}
func (f *fakeRunner) SetCtx(context.Context) {}
func (f *fakeRunner) Detach()                { f.detached = true }

// fakeProxmox is a no-op engine.Proxmox; only ClusterGuests (used by the create
// VM-ID conflict check) returns anything a test cares about.
type fakeProxmox struct {
	guests  []proxmox.Guest
	version string // reported by Version(); "" => unparseable (host-managed gate off)

	consoleErr    error                // returned by ConsoleExec
	consoleLogin  proxmox.ConsoleLogin // captured from the last ConsoleExec call
	consoleScript []string             // captured from the last ConsoleExec call
	consoleCalls  int

	agentPingErr error                   // returned by AgentPing
	agentResult  proxmox.AgentExecResult // returned by AgentExec
	agentExecErr error                   // returned by AgentExec
	agentCalls   int                     // AgentExec call count
	agentArgv    []string                // captured from the last AgentExec call
	agentInput   []byte                  // captured from the last AgentExec call

	snapshots     []proxmox.Snapshot // returned by Snapshots
	storageShared bool               // returned by StorageShared
	guestStatus   string             // returned by GuestStatus (e.g. "running")
	nodes         []proxmox.Node     // returned by Nodes

	rollbackStart  bool // captured "start" arg from the last RollbackSnapshot
	migrateRestart bool // captured "restart" arg from the last MigrateContainer
	migrateTo      string
	migrateCalls   int
}

func (f *fakeProxmox) ClusterGuests() ([]proxmox.Guest, error)                   { return f.guests, nil }
func (f *fakeProxmox) Version() (string, error)                                  { return f.version, nil }
func (f *fakeProxmox) Nodes() ([]proxmox.Node, error)                            { return f.nodes, nil }
func (f *fakeProxmox) AgentIPv4s(string, int) ([]string, error)                  { return nil, nil }
func (f *fakeProxmox) ContainerIPv4s(string, int) ([]string, error)              { return nil, nil }
func (f *fakeProxmox) VMConfig(string, int) (*proxmox.GuestConfig, error)        { return nil, nil }
func (f *fakeProxmox) ContainerConfig(string, int) (*proxmox.GuestConfig, error) { return nil, nil }
func (f *fakeProxmox) GuestStatus(string, string, int) (string, error)           { return f.guestStatus, nil }
func (f *fakeProxmox) StorageShared(string, string) (bool, error)                { return f.storageShared, nil }
func (f *fakeProxmox) Snapshots(string, string, int) ([]proxmox.Snapshot, error) {
	return f.snapshots, nil
}
func (f *fakeProxmox) CreateSnapshot(string, string, int, string, string, bool) (string, error) {
	return "upid", nil
}
func (f *fakeProxmox) RollbackSnapshot(_ string, _ string, _ int, _ string, start bool) (string, error) {
	f.rollbackStart = start
	return "upid", nil
}
func (f *fakeProxmox) DeleteSnapshot(string, string, int, string) (string, error) { return "upid", nil }
func (f *fakeProxmox) MigrateContainer(_ string, _ int, to string, restart bool) (string, error) {
	f.migrateCalls++
	f.migrateTo = to
	f.migrateRestart = restart
	return "upid", nil
}
func (f *fakeProxmox) ConsoleExec(_ string, _ int, script []string, login proxmox.ConsoleLogin) error {
	f.consoleCalls++
	f.consoleScript = script
	f.consoleLogin = login
	return f.consoleErr
}
func (f *fakeProxmox) AgentPing(string, int) error { return f.agentPingErr }
func (f *fakeProxmox) AgentExec(_ string, _ int, argv []string, input []byte) (proxmox.AgentExecResult, error) {
	f.agentCalls++
	f.agentArgv = argv
	f.agentInput = input
	return f.agentResult, f.agentExecErr
}
func (f *fakeProxmox) WaitTask(string, string, time.Duration) error { return nil }
func (f *fakeProxmox) RebootVM(string, int) error                   { return nil }
func (f *fakeProxmox) StartGuest(string, string, int) error         { return nil }
func (f *fakeProxmox) ShutdownGuest(string, string, int) error      { return nil }
func (f *fakeProxmox) StopGuest(string, string, int) error          { return nil }
func (f *fakeProxmox) RebootGuest(string, string, int) error        { return nil }

func newTestEngine(t *testing.T, r Runner, pm Proxmox) *Engine {
	t.Helper()
	store := state.New(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	return New(&config.Config{}, store, r, pm)
}

func TestCreateRollsBackOnApplyFailure(t *testing.T) {
	r := &fakeRunner{applyErr: errors.New("boom")}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}

	if _, err := e.Create(&wizard.Result{VM: vm}); err == nil {
		t.Fatal("expected Create to fail when Apply fails")
	}
	if r.applyCalls != 1 {
		t.Errorf("Apply calls = %d, want 1", r.applyCalls)
	}
	if r.destroyCalls != 1 {
		t.Errorf("Destroy calls = %d, want 1 (partial guest must be torn down)", r.destroyCalls)
	}
	if !r.detached {
		t.Error("rollback must Detach the runner so cleanup survives a cancelled context")
	}
	if _, err := e.Store.Load("web"); err == nil {
		t.Error("declaration must be deleted after a failed create")
	}
}

func TestCreateRejectsNameCollision(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	// A different VM ID so the vmid check passes; the NAME collision must still veto.
	dup := &state.VMSpec{Name: "web", VMID: 6200, Type: "vm", DHCP: true}
	_, err := e.Create(&wizard.Result{VM: dup})
	if err == nil {
		t.Fatal("expected Create to reject a name already managed")
	}
	if r.applyCalls != 0 {
		t.Errorf("Apply must not run on a name collision (ran %d times)", r.applyCalls)
	}
}

func TestAdoptVetoesReplaceAndRollsBack(t *testing.T) {
	r := &fakeRunner{planReplace: true, planSummary: "~ cpu.cores"}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "legacy", VMID: 6100, Type: "vm"}

	_, err := e.Adopt(vm)
	if err == nil {
		t.Fatal("expected Adopt to veto a plan that forces a replace")
	}
	if !strings.Contains(err.Error(), "replace") {
		t.Errorf("error should explain the replace veto, got: %v", err)
	}
	if r.destroyCalls != 0 {
		t.Fatalf("adopt must NEVER destroy the live guest (Destroy ran %d times)", r.destroyCalls)
	}
	if r.stateRmCalls != 1 {
		t.Errorf("expected the import to be rolled back via StateRm (ran %d times)", r.stateRmCalls)
	}
	if _, lerr := e.Store.Load("legacy"); lerr == nil {
		t.Error("declaration must be rolled back after a vetoed adoption")
	}
}

func TestAdoptReportsIncompleteRollback(t *testing.T) {
	r := &fakeRunner{planReplace: true, stateRmErr: errors.New("state locked")}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "legacy", VMID: 6100, Type: "vm"}

	_, err := e.Adopt(vm)
	if err == nil {
		t.Fatal("expected Adopt to fail")
	}
	if !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") {
		t.Errorf("a failed state rm must be surfaced as an incomplete rollback, got: %v", err)
	}
	if r.destroyCalls != 0 {
		t.Fatalf("the live guest must stay untouched even when rollback fails (Destroy ran %d times)", r.destroyCalls)
	}
}

func TestAdoptRollsBackOnWorkspacePrepFailure(t *testing.T) {
	// prepareWorkspace fails (Init errors) before Import is ever attempted: nothing
	// touched Terraform state, so only the declaration needs undoing — the live
	// guest is never imported, let alone destroyed.
	r := &fakeRunner{initErr: errors.New("terraform init failed")}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "legacy", VMID: 6100, Type: "vm"}

	_, err := e.Adopt(vm)
	if err == nil {
		t.Fatal("expected Adopt to fail when the workspace can't be prepared")
	}
	if r.importCalls != 0 {
		t.Errorf("Import must not run when prepareWorkspace fails (ran %d times)", r.importCalls)
	}
	if r.stateRmCalls != 0 || r.destroyCalls != 0 {
		t.Errorf("nothing reached Terraform state, so no StateRm/Destroy (stateRm=%d destroy=%d)", r.stateRmCalls, r.destroyCalls)
	}
	if !r.detached {
		t.Error("rollback must Detach so cleanup survives a cancelled context")
	}
	if _, lerr := e.Store.Load("legacy"); lerr == nil {
		t.Error("declaration must be rolled back after a workspace-prep failure")
	}
}

func TestAdoptSucceedsOnCleanPlan(t *testing.T) {
	r := &fakeRunner{planChanges: false, planReplace: false}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "legacy", VMID: 6100, Type: "vm"}

	drift, err := e.Adopt(vm)
	if err != nil {
		t.Fatalf("clean adoption should succeed: %v", err)
	}
	if drift != "" {
		t.Errorf("a clean plan should report no drift, got %q", drift)
	}
	if r.stateRmCalls != 0 || r.destroyCalls != 0 {
		t.Errorf("no rollback on success (stateRm=%d destroy=%d)", r.stateRmCalls, r.destroyCalls)
	}
	if _, lerr := e.Store.Load("legacy"); lerr != nil {
		t.Errorf("the adopted declaration must persist: %v", lerr)
	}
	if r.importCalls != 1 {
		t.Errorf("expected exactly one Import (ran %d times)", r.importCalls)
	}
}
