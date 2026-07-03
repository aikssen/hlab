package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/state"
)

func TestResourceAddr(t *testing.T) {
	tests := []struct {
		name string
		spec *state.VMSpec
		want string
	}{
		{"vm", &state.VMSpec{Name: "web-01"}, `proxmox_virtual_environment_vm.vm["web-01"]`},
		{"explicit vm type", &state.VMSpec{Name: "web-01", Type: "vm"}, `proxmox_virtual_environment_vm.vm["web-01"]`},
		{"lxc", &state.VMSpec{Name: "ct-01", Type: "lxc"}, `proxmox_virtual_environment_container.ct["ct-01"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resourceAddr(tt.spec); got != tt.want {
				t.Errorf("resourceAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAdoptedStateAttrs(t *testing.T) {
	t.Run("VM", func(t *testing.T) {
		vm := &state.VMSpec{Name: "web", VMID: 6100}
		attrs := AdoptedStateAttrs(vm)
		if attrs["vm_id"] != 6100 {
			t.Errorf("vm_id = %v, want 6100", attrs["vm_id"])
		}
		if attrs["migrate"] != true {
			t.Errorf("migrate = %v, want true (matches main.tf)", attrs["migrate"])
		}
		for _, k := range []string{"timeout_clone", "timeout_create", "timeout_migrate",
			"timeout_move_disk", "timeout_reboot", "timeout_shutdown_vm", "timeout_start_vm", "timeout_stop_vm"} {
			if _, ok := attrs[k]; !ok {
				t.Errorf("VM AdoptedStateAttrs missing key %q", k)
			}
		}
		// LXC-only timeout keys must not appear on a VM.
		if _, ok := attrs["timeout_delete"]; ok {
			t.Errorf("VM AdoptedStateAttrs should not include timeout_delete")
		}
	})

	t.Run("LXC", func(t *testing.T) {
		ct := &state.VMSpec{Name: "ct", Type: "lxc", VMID: 6101}
		attrs := AdoptedStateAttrs(ct)
		if attrs["vm_id"] != 6101 {
			t.Errorf("vm_id = %v, want 6101", attrs["vm_id"])
		}
		if _, ok := attrs["migrate"]; ok {
			t.Errorf("LXC AdoptedStateAttrs should not include migrate (bpg containers have no migrate attribute)")
		}
		for _, k := range []string{"timeout_create", "timeout_clone", "timeout_update", "timeout_delete", "timeout_start"} {
			if _, ok := attrs[k]; !ok {
				t.Errorf("LXC AdoptedStateAttrs missing key %q", k)
			}
		}
	})
}

func TestJoin(t *testing.T) {
	tests := []struct{ path, key, want string }{
		{"", "cpu", "cpu"},
		{"cpu", "cores", "cpu.cores"},
		{"a.b", "c", "a.b.c"},
	}
	for _, tt := range tests {
		if got := join(tt.path, tt.key); got != tt.want {
			t.Errorf("join(%q, %q) = %q, want %q", tt.path, tt.key, got, tt.want)
		}
	}
}

func TestDedup(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"no dupes preserves order", []string{"b", "a", "c"}, []string{"b", "a", "c"}},
		{"removes dupes preserving first occurrence order", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedup(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("dedup(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("dedup(%v)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEqualJSON(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want bool
	}{
		{"equal strings", "x", "x", true},
		{"different strings", "x", "y", false},
		{"numeric equal despite type differences", float64(2), float64(2), true},
		{"nil equal", nil, nil, true},
		{"nil vs value differ", nil, "x", false},
		{"maps with different key order are equal", map[string]any{"a": 1, "b": 2}, map[string]any{"b": 2, "a": 1}, true},
		{"maps with different values differ", map[string]any{"a": 1}, map[string]any{"a": 2}, false},
		{"slices equal", []any{1, 2}, []any{1, 2}, true},
		{"slices in different order differ", []any{1, 2}, []any{2, 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := equalJSON(tt.a, tt.b); got != tt.want {
				t.Errorf("equalJSON(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDriftIgnore(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{"migrate", true},
		{"started", true},
		{"cpu.cores", false}, // real drift must NOT be ignored
		{"memory.dedicated", false},
		{"timeout_create", true},
		{"nested.timeout_delete", true}, // prefix match on any segment
		{"initialization.ip_config.ipv6", true},
		{"initialization.ip_config.ipv4", false}, // only the ipv6 suffix is ignored
		{"features.keyctl", true},
		{"features.nesting", false}, // hlab manages nesting; must remain visible
		{"tags", true},
		{"description", true},
		{"disk.size", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := driftIgnore(tt.path); got != tt.want {
				t.Errorf("driftIgnore(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDriftPathsScalarChange(t *testing.T) {
	got := driftPaths(2, 4, nil, "cpu.cores")
	want := []string{"cpu.cores"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("driftPaths(scalar change) = %v, want %v", got, want)
	}
}

func TestDriftPathsScalarUnchanged(t *testing.T) {
	got := driftPaths(2, 2, nil, "cpu.cores")
	if len(got) != 0 {
		t.Errorf("driftPaths(unchanged scalar) = %v, want empty", got)
	}
}

func TestDriftPathsPrunesIgnoredPath(t *testing.T) {
	got := driftPaths("stopped", "running", nil, "started")
	if len(got) != 0 {
		t.Errorf("driftPaths(ignored path) = %v, want empty (started is bookkeeping)", got)
	}
}

func TestDriftPathsPrunesComputedLeaf(t *testing.T) {
	// after_unknown == true means "known after apply" — a computed leaf, not
	// real drift, even though before != after.
	got := driftPaths(nil, "10.0.0.5", true, "ipv4_address")
	if len(got) != 0 {
		t.Errorf("driftPaths(computed leaf) = %v, want empty", got)
	}
}

func TestDriftPathsRecursesIntoNestedComputedBlock(t *testing.T) {
	// A nested block reports after_unknown as a map (not a bare `true`), so it
	// must be recursed into rather than pruned wholesale — this is exactly how
	// real drift like cpu.cores surfaces despite cpu being partly computed.
	before := map[string]any{"cores": float64(2), "type": "host"}
	after := map[string]any{"cores": float64(4), "type": "host"}
	unknown := map[string]any{"cores": false, "type": false}
	got := driftPaths(before, after, unknown, "cpu")
	if len(got) != 1 || got[0] != "cpu.cores" {
		t.Errorf("driftPaths(nested computed block) = %v, want [cpu.cores]", got)
	}
}

func TestDriftPathsEmptyVsNonEmptyListIsNotDrift(t *testing.T) {
	// An adopted-but-never-applied guest has an empty `[]` block in state; the
	// config side is non-empty. That's a state-representation gap, not drift.
	before := []any{}
	after := []any{map[string]any{"cores": float64(2)}}
	got := driftPaths(before, after, nil, "cpu")
	if len(got) != 0 {
		t.Errorf("driftPaths(empty before, non-empty after) = %v, want empty", got)
	}

	got2 := driftPaths(after, before, nil, "cpu")
	if len(got2) != 0 {
		t.Errorf("driftPaths(non-empty before, empty after) = %v, want empty", got2)
	}
}

func TestDriftPathsTwoNonEmptyListsOfDifferentLengthIsRealDrift(t *testing.T) {
	before := []any{map[string]any{"size": float64(32)}}
	after := []any{
		map[string]any{"size": float64(32)},
		map[string]any{"size": float64(64)},
	}
	got := driftPaths(before, after, nil, "disk")
	if len(got) != 1 || got[0] != "disk" {
		t.Errorf("driftPaths(list length mismatch, both non-empty) = %v, want [disk]", got)
	}
}

func TestDriftPathsListsSameLengthRecurses(t *testing.T) {
	before := []any{map[string]any{"ipv4": "192.168.1.10"}}
	after := []any{map[string]any{"ipv4": "192.168.1.20"}}
	got := driftPaths(before, after, nil, "initialization.ip_config")
	if len(got) != 1 || got[0] != "initialization.ip_config.ipv4" {
		t.Errorf("driftPaths(same-length lists) = %v, want [initialization.ip_config.ipv4]", got)
	}
}

func TestDriftPathsMapKeyUnion(t *testing.T) {
	// A key present only in `before` or only in `after` must still be diffed
	// (the key union, not intersection).
	before := map[string]any{"a": "1"}
	after := map[string]any{"b": "2"}
	got := driftPaths(before, after, nil, "")
	if len(got) != 2 {
		t.Fatalf("driftPaths(key union) = %v, want 2 entries", got)
	}
}

func TestExistingPasswordsNoFile(t *testing.T) {
	r := &Runner{Dir: t.TempDir()}
	pw, err := r.ExistingPasswords()
	if err != nil {
		t.Fatalf("ExistingPasswords() error: %v", err)
	}
	if len(pw) != 0 {
		t.Errorf("ExistingPasswords() = %v, want empty map when no secrets file exists", pw)
	}
}

func TestExistingPasswordsMergesVMAndCT(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Dir: dir}
	secrets := map[string]any{
		"vm_passwords": map[string]string{"web-01": "vmpw"},
		"ct_passwords": map[string]string{"ct-01": "ctpw"},
	}
	data, err := json.Marshal(secrets)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.auto.tfvars.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pw, err := r.ExistingPasswords()
	if err != nil {
		t.Fatalf("ExistingPasswords() error: %v", err)
	}
	if pw["web-01"] != "vmpw" || pw["ct-01"] != "ctpw" {
		t.Errorf("ExistingPasswords() = %v, want merged vm+ct passwords", pw)
	}
}

func TestSyncWritesTfvarsAndSecrets(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Dir: dir, Cfg: &config.Config{}}

	vms := []*state.VMSpec{
		{Name: "web-01", Type: "vm", Node: "pve1", VMID: 6100, Cores: 2, MemoryGB: 4, DiskGB: 32, DHCP: true, Username: "admin"},
		{Name: "ct-01", Type: "lxc", Node: "pve2", VMID: 6101, Cores: 1, MemoryMB: 512, DiskGB: 4, DHCP: false, IPCIDR: "192.168.1.101/24"},
	}
	passwords := map[string]string{"web-01": "vm-secret", "ct-01": "ct-secret"}

	if err := r.Sync(vms, passwords); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// tfvars: partitions VMs/CTs into their respective maps.
	tfvarsData, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	if err != nil {
		t.Fatalf("reading terraform.tfvars.json: %v", err)
	}
	var tfvars struct {
		VMs map[string]tfVM `json:"vms"`
		CTs map[string]tfCT `json:"cts"`
	}
	if err := json.Unmarshal(tfvarsData, &tfvars); err != nil {
		t.Fatalf("unmarshaling tfvars: %v", err)
	}
	vmEntry, ok := tfvars.VMs["web-01"]
	if !ok {
		t.Fatalf("tfvars.vms missing web-01: %+v", tfvars.VMs)
	}
	if vmEntry.MemoryMB != 4096 || vmEntry.VMID != 6100 || vmEntry.Node != "pve1" {
		t.Errorf("web-01 tfVM = %+v, want MemoryMB=4096 VMID=6100 Node=pve1", vmEntry)
	}
	ctEntry, ok := tfvars.CTs["ct-01"]
	if !ok {
		t.Fatalf("tfvars.cts missing ct-01: %+v", tfvars.CTs)
	}
	if ctEntry.MemoryMB != 512 || ctEntry.VMID != 6101 {
		t.Errorf("ct-01 tfCT = %+v, want MemoryMB=512 VMID=6101", ctEntry)
	}
	if _, wrongMap := tfvars.VMs["ct-01"]; wrongMap {
		t.Error("ct-01 must not land in the vms map")
	}
	if _, wrongMap := tfvars.CTs["web-01"]; wrongMap {
		t.Error("web-01 must not land in the cts map")
	}

	// secrets: partitioned by guest type, gitignored file mode 0600.
	secretsPath := filepath.Join(dir, "secrets.auto.tfvars.json")
	info, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("reading secrets file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.auto.tfvars.json permission = %v, want 0600", perm)
	}
	merged, err := r.ExistingPasswords()
	if err != nil {
		t.Fatalf("ExistingPasswords() error: %v", err)
	}
	if merged["web-01"] != "vm-secret" || merged["ct-01"] != "ct-secret" {
		t.Errorf("round-tripped passwords = %v, want the originals", merged)
	}

	// materialize should have copied at least one embedded .tf file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	foundTF := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tf" {
			foundTF = true
			break
		}
	}
	if !foundTF {
		t.Error("Sync() should have materialized at least one .tf file into the working directory")
	}
}

// TestSyncRendersNoSSHKeysWhenNone traces the keyless ("none") create path: a
// declaration with no SSH keys must render ssh_keys as an empty/absent list in
// tfvars, never silently backfilled with a configured default. main.tf/
// container.tf then render user_account.keys from exactly this list, so a keyless
// guest really has zero keys injected — proving hlab is not the source of a
// "phantom" key on a VM created with ssh-key = none (that comes from the golden
// image's baked-in authorized_keys, which cloud-init merges and hlab can't strip).
func TestSyncRendersNoSSHKeysWhenNone(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Dir: dir, Cfg: &config.Config{}}
	vms := []*state.VMSpec{
		// VM created with "none": password login only, SSHKeys nil.
		{Name: "web", Type: "vm", Node: "pve1", VMID: 6100, Cores: 2, MemoryGB: 4, DiskGB: 32, DHCP: true, Username: "admin", HasPassword: true},
		// Container created with "none": root password only, SSHKeys nil.
		{Name: "dns", Type: "lxc", Node: "pve1", VMID: 6101, Cores: 1, MemoryMB: 512, DiskGB: 4, DHCP: false, IPCIDR: "192.168.1.101/24", HasPassword: true},
	}
	if err := r.Sync(vms, map[string]string{"web": "pw", "dns": "pw"}); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	if err != nil {
		t.Fatalf("reading terraform.tfvars.json: %v", err)
	}
	var tfvars struct {
		VMs map[string]tfVM `json:"vms"`
		CTs map[string]tfCT `json:"cts"`
	}
	if err := json.Unmarshal(data, &tfvars); err != nil {
		t.Fatalf("unmarshaling tfvars: %v", err)
	}
	if got := tfvars.VMs["web"].SSHKeys; len(got) != 0 {
		t.Errorf("keyless VM rendered ssh_keys = %v, want none (no default backfilled)", got)
	}
	if got := tfvars.CTs["dns"].SSHKeys; len(got) != 0 {
		t.Errorf("keyless container rendered ssh_keys = %v, want none", got)
	}
}

func TestSyncRendersHostManagedOnlyWhenSet(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Dir: dir, Cfg: &config.Config{}}
	vms := []*state.VMSpec{
		// static-IP CT on PVE 9.1+ opts in → host_managed rendered as true
		{Name: "ct-hm", Type: "lxc", Node: "pve1", VMID: 6101, Cores: 1, MemoryMB: 512, DiskGB: 4, DHCP: false, IPCIDR: "192.168.1.101/24", HostManagedNet: true},
		// unset → key must be absent so optional(bool) yields null (never sent to older Proxmox)
		{Name: "ct-plain", Type: "lxc", Node: "pve1", VMID: 6102, Cores: 1, MemoryMB: 512, DiskGB: 4, DHCP: true},
	}
	if err := r.Sync(vms, nil); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	if err != nil {
		t.Fatalf("reading terraform.tfvars.json: %v", err)
	}
	// Decode the cts map loosely so we can assert on key presence, not just value.
	var doc struct {
		CTs map[string]map[string]json.RawMessage `json:"cts"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshaling tfvars: %v", err)
	}
	hm, ok := doc.CTs["ct-hm"]["host_managed"]
	if !ok {
		t.Fatal("ct-hm should render host_managed when HostManagedNet is set")
	}
	if string(hm) != "true" {
		t.Errorf("ct-hm host_managed = %s, want true", hm)
	}
	if _, present := doc.CTs["ct-plain"]["host_managed"]; present {
		t.Error("ct-plain (HostManagedNet unset) must omit host_managed so optional() yields null")
	}
}

func TestSyncWithNoPasswordsRemovesSecretsFile(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Dir: dir, Cfg: &config.Config{}}
	vms := []*state.VMSpec{{Name: "web-01", Node: "pve1", VMID: 6100}}

	// First sync with a password, creating the secrets file.
	if err := r.Sync(vms, map[string]string{"web-01": "secret"}); err != nil {
		t.Fatalf("first Sync() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.auto.tfvars.json")); err != nil {
		t.Fatalf("expected secrets file to exist after first Sync(): %v", err)
	}

	// A second sync with no passwords at all should remove the file rather
	// than leave stale secrets behind.
	if err := r.Sync(vms, map[string]string{}); err != nil {
		t.Fatalf("second Sync() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.auto.tfvars.json")); !os.IsNotExist(err) {
		t.Errorf("expected secrets file to be removed when there are no passwords, stat err = %v", err)
	}
}
