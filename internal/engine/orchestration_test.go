package engine

import (
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
)

func TestFirstIPv4(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{"first usable", []string{"192.168.1.50", "192.168.1.51"}, "192.168.1.50"},
		{"skips loopback", []string{"127.0.0.1", "192.168.1.50"}, "192.168.1.50"},
		{"skips ipv6", []string{"fe80::1", "192.168.1.50"}, "192.168.1.50"},
		{"skips empty entries", []string{"", "192.168.1.50"}, "192.168.1.50"},
		{"none usable", []string{"127.0.0.1", "::1", ""}, ""},
		{"nil slice", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirstIPv4(c.addrs); got != c.want {
				t.Errorf("FirstIPv4(%v) = %q, want %q", c.addrs, got, c.want)
			}
		})
	}
}

func TestDeclaredIP(t *testing.T) {
	cases := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"static strips prefix", &state.VMSpec{DHCP: false, IPCIDR: "192.168.1.50/24"}, "192.168.1.50"},
		{"dhcp yields empty", &state.VMSpec{DHCP: true, IPCIDR: "192.168.1.50/24"}, ""},
		{"static but no cidr", &state.VMSpec{DHCP: false}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeclaredIP(c.vm); got != c.want {
				t.Errorf("DeclaredIP() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestResolveIPStaticIsAuthoritative: a static VM reports its declared IP without
// ever consulting the guest agent / terraform output.
func TestResolveIPStaticIsAuthoritative(t *testing.T) {
	e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", Type: "vm", DHCP: false, IPCIDR: "192.168.1.50/24"}
	if got := e.ResolveIP(vm); got != "192.168.1.50" {
		t.Errorf("ResolveIP(static) = %q, want the declared IP", got)
	}
}

// TestResolveIPVMFromTerraform: a DHCP VM with no declared IP falls back to the
// terraform output (guest-agent list), picking the first usable IPv4.
func TestResolveIPVMFromTerraform(t *testing.T) {
	r := &fakeRunner{ips: map[string][]string{"web": {"127.0.0.1", "192.168.1.77"}}}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", Type: "vm", DHCP: true}
	if got := e.ResolveIP(vm); got != "192.168.1.77" {
		t.Errorf("ResolveIP(dhcp VM) = %q, want 192.168.1.77", got)
	}
}

// TestDetectDriftClassifies exercises the classification logic: each DriftReport
// action maps to the right DriftStatus.State, a declaration with no report row is
// "in-sync", and a report row with no declaration surfaces as "orphaned". Results
// are sorted by name.
func TestDetectDriftClassifies(t *testing.T) {
	r := &fakeRunner{driftChanges: []terraform.DriftChange{
		{Name: "web", Action: "update", Attrs: []string{"cpu.cores"}},
		{Name: "db", Action: "replace"},
		{Name: "cache", Action: "create"},
		{Name: "ghost", Action: "delete", IsLXC: true}, // no declaration → orphaned
	}}
	e := newTestEngine(t, r, &fakeProxmox{})
	for _, vm := range []*state.VMSpec{
		{Name: "web", VMID: 6100, Type: "vm"},
		{Name: "db", VMID: 6101, Type: "vm"},
		{Name: "cache", VMID: 6102, Type: "lxc"},
		{Name: "quiet", VMID: 6103, Type: "vm"}, // no report row → in-sync
	} {
		if err := e.Store.Save(vm); err != nil {
			t.Fatalf("seed %s: %v", vm.Name, err)
		}
	}

	statuses, err := e.DetectDrift()
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	byName := map[string]DriftStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}
	want := map[string]string{
		"web":   "in-place",
		"db":    "replace",
		"cache": "missing",
		"quiet": "in-sync",
		"ghost": "orphaned",
	}
	for name, state := range want {
		if got := byName[name].State; got != state {
			t.Errorf("%s drift state = %q, want %q", name, got, state)
		}
	}
	if byName["cache"].Kind != "lxc" {
		t.Errorf("cache kind = %q, want lxc", byName["cache"].Kind)
	}
	if byName["ghost"].Kind != "lxc" {
		t.Errorf("orphan kind should come from the report IsLXC flag, got %q", byName["ghost"].Kind)
	}
	// Sorted ascending by name.
	for i := 1; i < len(statuses); i++ {
		if statuses[i-1].Name > statuses[i].Name {
			t.Fatalf("statuses not sorted by name: %v", statuses)
		}
	}
}

// TestDetectDriftScopedIgnoresOrphans: a targeted plan classifies only the target
// and never emits orphan rows (a scoped plan targets known declarations by name).
func TestDetectDriftScopedIgnoresOrphans(t *testing.T) {
	r := &fakeRunner{driftChanges: []terraform.DriftChange{
		{Name: "web", Action: "update"},
		{Name: "stray", Action: "delete"},
	}}
	e := newTestEngine(t, r, &fakeProxmox{})
	web := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm"}
	if err := e.Store.Save(web); err != nil {
		t.Fatalf("seed: %v", err)
	}

	statuses, err := e.DetectDrift(web)
	if err != nil {
		t.Fatalf("DetectDrift(target): %v", err)
	}
	if len(statuses) != 1 || statuses[0].Name != "web" || statuses[0].State != "in-place" {
		t.Errorf("scoped drift = %+v, want just web/in-place (no orphan rows)", statuses)
	}
}

// TestReconfigureRejectsDiskShrink pins the disk-can-only-grow business rule: a
// smaller disk is refused before anything is applied, and the declaration is left
// unchanged.
func TestReconfigureRejectsDiskShrink(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	orig := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Cores: 2, MemoryGB: 4, DiskGB: 32}
	if err := e.Store.Save(orig); err != nil {
		t.Fatalf("seed: %v", err)
	}

	shrink := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Cores: 2, MemoryGB: 4, DiskGB: 16}
	err := e.Reconfigure(shrink)
	if err == nil || !strings.Contains(err.Error(), "disk can only grow") {
		t.Fatalf("Reconfigure(shrink) should be rejected, got: %v", err)
	}
	if r.applyCalls != 0 {
		t.Errorf("a rejected shrink must not apply (applied %d)", r.applyCalls)
	}
	got, _ := e.Store.Load("web")
	if got.DiskGB != 32 {
		t.Errorf("declaration disk should be untouched at 32, got %d", got.DiskGB)
	}
}

// TestReconfigureGrowsDiskAndApplies: a larger disk is accepted and applied.
func TestReconfigureGrowsDiskAndApplies(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	orig := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DiskGB: 32}
	if err := e.Store.Save(orig); err != nil {
		t.Fatalf("seed: %v", err)
	}
	grow := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DiskGB: 64}
	if err := e.Reconfigure(grow); err != nil {
		t.Fatalf("Reconfigure(grow): %v", err)
	}
	if r.applyCalls != 1 {
		t.Errorf("a valid resize must apply once (applied %d)", r.applyCalls)
	}
	got, _ := e.Store.Load("web")
	if got.DiskGB != 64 {
		t.Errorf("declaration disk should be 64, got %d", got.DiskGB)
	}
}

// TestSnapshotLXCWithRAMVetoed: an LXC snapshot cannot include RAM state.
func TestSnapshotLXCWithRAMVetoed(t *testing.T) {
	e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{})
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := e.Snapshot("dns", "snap1", "", true); err == nil ||
		!strings.Contains(err.Error(), "cannot include RAM") {
		t.Fatalf("LXC snapshot with RAM should be vetoed, got: %v", err)
	}
}

// TestRollbackSnapshotRestartsRunningGuest: if the guest was running, the rollback
// asks Proxmox to start it again afterwards (a RAM-less snapshot otherwise leaves
// it stopped); a stopped guest is left stopped.
func TestRollbackSnapshotRestartsRunningGuest(t *testing.T) {
	t.Run("running is restarted", func(t *testing.T) {
		pm := &fakeProxmox{guestStatus: "running"}
		e := newTestEngine(t, &fakeRunner{}, pm)
		if err := e.Store.Save(&state.VMSpec{Name: "web", VMID: 6100, Type: "vm"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := e.RollbackSnapshot("web", "snap1"); err != nil {
			t.Fatalf("RollbackSnapshot: %v", err)
		}
		if !pm.rollbackStart {
			t.Error("a running guest should be restarted after rollback")
		}
	})
	t.Run("stopped stays stopped", func(t *testing.T) {
		pm := &fakeProxmox{guestStatus: "stopped"}
		e := newTestEngine(t, &fakeRunner{}, pm)
		if err := e.Store.Save(&state.VMSpec{Name: "web", VMID: 6100, Type: "vm"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := e.RollbackSnapshot("web", "snap1"); err != nil {
			t.Fatalf("RollbackSnapshot: %v", err)
		}
		if pm.rollbackStart {
			t.Error("a stopped guest must not be started after rollback")
		}
	})
}

func TestMigrateValidation(t *testing.T) {
	seed := func(t *testing.T, e *Engine) {
		t.Helper()
		if err := e.Store.Save(&state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Node: "pve1"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("empty target", func(t *testing.T) {
		e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{})
		seed(t, e)
		if err := e.Migrate("web", ""); err == nil {
			t.Fatal("empty target node should be rejected")
		}
	})

	t.Run("same node", func(t *testing.T) {
		e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{})
		seed(t, e)
		if err := e.Migrate("web", "pve1"); err == nil ||
			!strings.Contains(err.Error(), "already on node") {
			t.Fatalf("migrating to the current node should be rejected, got: %v", err)
		}
	})

	t.Run("unknown target", func(t *testing.T) {
		pm := &fakeProxmox{nodes: []proxmox.Node{{Name: "pve1"}, {Name: "pve2"}}}
		e := newTestEngine(t, &fakeRunner{}, pm)
		seed(t, e)
		if err := e.Migrate("web", "mars"); err == nil ||
			!strings.Contains(err.Error(), "not found in the cluster") {
			t.Fatalf("an unknown target node should be rejected, got: %v", err)
		}
	})
}

// TestMigrateContainerSnapshotPreflight: a container with snapshots on non-shared
// storage is refused up front (LVM-thin snapshots can't migrate), and the move is
// never attempted.
func TestMigrateContainerSnapshotPreflight(t *testing.T) {
	pm := &fakeProxmox{
		nodes:         []proxmox.Node{{Name: "pve1"}, {Name: "pve2"}},
		snapshots:     []proxmox.Snapshot{{Name: "snap1"}},
		storageShared: false, // local storage
	}
	e := newTestEngine(t, &fakeRunner{}, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Node: "pve1", Storage: "local-lvm"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := e.Migrate("dns", "pve2")
	if err == nil || !strings.Contains(err.Error(), "non-shared storage") {
		t.Fatalf("a container with snapshots on local storage should be refused, got: %v", err)
	}
	if pm.migrateCalls != 0 {
		t.Errorf("the migration must not be attempted after the preflight veto (called %d)", pm.migrateCalls)
	}
}

// TestMigrateContainerReanchorsState: with no blocking snapshots, a running
// container is migrated (restart=1) and Terraform state is re-anchored to the new
// node via SetResourceNode (never a state rm + import).
func TestMigrateContainerReanchorsState(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{
		nodes:       []proxmox.Node{{Name: "pve1"}, {Name: "pve2"}},
		guestStatus: "running",
	}
	e := newTestEngine(t, r, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Node: "pve1", Storage: "local-lvm"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := e.Migrate("dns", "pve2"); err != nil {
		t.Fatalf("Migrate(container): %v", err)
	}
	if pm.migrateCalls != 1 || pm.migrateTo != "pve2" || !pm.migrateRestart {
		t.Errorf("expected one API migration to pve2 with restart=1, got calls=%d to=%q restart=%v",
			pm.migrateCalls, pm.migrateTo, pm.migrateRestart)
	}
	if r.setNodeCalls != 1 || r.setNodeTo != "pve2" {
		t.Errorf("state must be re-anchored via SetResourceNode(pve2), got calls=%d to=%q", r.setNodeCalls, r.setNodeTo)
	}
	if r.stateRmCalls != 0 {
		t.Errorf("container migration must not state-rm/import (state rm ran %d)", r.stateRmCalls)
	}
	got, _ := e.Store.Load("dns")
	if got.Node != "pve2" {
		t.Errorf("declaration node = %q, want pve2", got.Node)
	}
}

// TestInjectSSHKeyViaAgentUserFallback covers the user-resolution branches: an
// empty vm.Username falls back to the config DefaultUser, then to root.
func TestInjectSSHKeyViaAgentUserFallback(t *testing.T) {
	t.Run("config default user", func(t *testing.T) {
		r := &fakeRunner{}
		pm := &fakeProxmox{agentResult: proxmox.AgentExecResult{ExitCode: 0}}
		store := state.New(t.TempDir())
		if err := store.Init(); err != nil {
			t.Fatalf("store init: %v", err)
		}
		e := New(&config.Config{DefaultUser: "operator"}, store, r, pm)
		vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true} // no Username
		if err := e.Store.Save(vm); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := e.InjectSSHKeyViaAgent(vm, testPubKey); err != nil {
			t.Fatalf("inject: %v", err)
		}
		if !strings.Contains(pm.agentArgv[2], "user='operator'") {
			t.Errorf("script should target the config default user, got: %s", pm.agentArgv[2])
		}
	})

	t.Run("root fallback", func(t *testing.T) {
		r := &fakeRunner{}
		pm := &fakeProxmox{agentResult: proxmox.AgentExecResult{ExitCode: 0}}
		e := newTestEngine(t, r, pm) // empty config → no DefaultUser
		vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
		if err := e.Store.Save(vm); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := e.InjectSSHKeyViaAgent(vm, testPubKey); err != nil {
			t.Fatalf("inject: %v", err)
		}
		if !strings.Contains(pm.agentArgv[2], "user='root'") {
			t.Errorf("script should fall back to root, got: %s", pm.agentArgv[2])
		}
	})
}
