package engine

import (
	"context"
	"io"
	"time"

	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
)

// Runner is the Terraform-facing behavior the engine orchestrates. The interface
// is owned by the consumer (engine), not the implementer: *terraform.Runner is
// the production implementation, and tests inject a fake to exercise the rollback
// branches without shelling out to real terraform. SetOut/SetCtx let a caller
// stream output and bind cancellation without reaching into the concrete Runner's
// fields.
type Runner interface {
	Sync(vms []*state.VMSpec, passwords map[string]string) error
	Init() error
	Apply(target *state.VMSpec) error
	Destroy(target *state.VMSpec) error
	Plan() error
	Refresh() error
	IPAddresses() map[string][]string
	ExistingPasswords() (map[string]string, error)
	Import(target *state.VMSpec) error
	PatchResourceAttrs(target *state.VMSpec, attrs map[string]any) error
	SetResourceNode(target *state.VMSpec, node string) error
	StateRm(target *state.VMSpec) error
	PlanDetailed(target *state.VMSpec) (changes, replace bool, summary string, err error)
	DriftReport(targets ...*state.VMSpec) ([]terraform.DriftChange, error)
	SetOut(io.Writer)
	SetCtx(context.Context)
	// Detach unbinds any cancellation context so cleanup/rollback still runs after
	// the original operation was cancelled.
	Detach()
}

// Proxmox is the read-only discovery plus power/snapshot behavior the engine
// depends on. *proxmox.Client is the production implementation; tests inject a
// fake. Discovery stays read-only; every mutation the engine performs still goes
// through the Runner (Terraform) or these explicit power/snapshot calls.
type Proxmox interface {
	Version() (string, error)
	Nodes() ([]proxmox.Node, error)
	ClusterGuests() ([]proxmox.Guest, error)
	AgentIPv4s(node string, vmid int) ([]string, error)
	ContainerIPv4s(node string, vmid int) ([]string, error)
	VMConfig(node string, vmid int) (*proxmox.GuestConfig, error)
	ContainerConfig(node string, vmid int) (*proxmox.GuestConfig, error)
	GuestStatus(node, kind string, vmid int) (string, error)
	StorageShared(node, name string) (bool, error)
	Snapshots(node, kind string, vmid int) ([]proxmox.Snapshot, error)
	CreateSnapshot(node, kind string, vmid int, name, description string, withRAM bool) (string, error)
	RollbackSnapshot(node, kind string, vmid int, name string, start bool) (string, error)
	DeleteSnapshot(node, kind string, vmid int, name string) (string, error)
	MigrateContainer(node string, vmid int, target string, restart bool) (string, error)
	ConsoleExec(node string, vmid int, script []string, login proxmox.ConsoleLogin) error
	AgentPing(node string, vmid int) error
	AgentExec(node string, vmid int, argv []string, inputData []byte) (proxmox.AgentExecResult, error)
	WaitTask(node, upid string, timeout time.Duration) error
	RebootVM(node string, vmid int) error
	StartGuest(node, kind string, vmid int) error
	ShutdownGuest(node, kind string, vmid int) error
	StopGuest(node, kind string, vmid int) error
	RebootGuest(node, kind string, vmid int) error
}

// Compile-time assertions that the production types satisfy the interfaces.
var (
	_ Runner  = (*terraform.Runner)(nil)
	_ Proxmox = (*proxmox.Client)(nil)
)
