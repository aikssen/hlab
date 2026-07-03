package cmd

import (
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/state"
)

func TestResolveVMNameByName(t *testing.T) {
	store := state.New(t.TempDir())
	if err := store.Save(&state.VMSpec{Name: "web-01", VMID: 6100}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := resolveVMName(store, "web-01")
	if err != nil {
		t.Fatalf("resolveVMName(name) error: %v", err)
	}
	if got != "web-01" {
		t.Errorf("resolveVMName(name) = %q, want web-01", got)
	}
}

func TestResolveVMNameByNumericID(t *testing.T) {
	store := state.New(t.TempDir())
	if err := store.Save(&state.VMSpec{Name: "web-01", VMID: 6100}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := resolveVMName(store, "6100")
	if err != nil {
		t.Fatalf("resolveVMName(id) error: %v", err)
	}
	if got != "web-01" {
		t.Errorf("resolveVMName(id) = %q, want web-01", got)
	}
}

func TestResolveVMNameUnknownName(t *testing.T) {
	store := state.New(t.TempDir())
	if _, err := resolveVMName(store, "ghost"); err == nil {
		t.Fatal("resolveVMName(unknown name) should error")
	}
}

func TestResolveVMNameUnknownID(t *testing.T) {
	store := state.New(t.TempDir())
	if err := store.Save(&state.VMSpec{Name: "web-01", VMID: 6100}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if _, err := resolveVMName(store, "9999"); err == nil {
		t.Fatal("resolveVMName(unknown id) should error")
	}
}

func TestResolveVMNameEmptyStore(t *testing.T) {
	store := state.New(t.TempDir())
	if _, err := resolveVMName(store, "6100"); err == nil {
		t.Fatal("resolveVMName on an empty store should error, not panic")
	}
	if _, err := resolveVMName(store, "anything"); err == nil {
		t.Fatal("resolveVMName by name on an empty store should error, not panic")
	}
}

func TestRamDisplay(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"LXC always shows MB", &state.VMSpec{Type: "lxc", MemoryMB: 512}, "512 MB"},
		{"VM with whole GB", &state.VMSpec{MemoryGB: 4}, "4 GB"},
		{"VM with odd MB falls back to decimal GB", &state.VMSpec{MemoryMB: 2560}, "2.5 GB"},
		{"VM with nothing set", &state.VMSpec{}, "0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ramDisplay(tt.vm); got != tt.want {
				t.Errorf("ramDisplay() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRamGBDisplay(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"MemoryGB set", &state.VMSpec{MemoryGB: 8}, "8"},
		{"MemoryGB unset, MemoryMB whole GB", &state.VMSpec{MemoryMB: 2048}, "2"},
		{"MemoryGB unset, MemoryMB fractional", &state.VMSpec{MemoryMB: 1536}, "1.5"},
		{"nothing set", &state.VMSpec{}, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ramGBDisplay(tt.vm); got != tt.want {
				t.Errorf("ramGBDisplay() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderResultLabelsNoWrap guards against the label column being too narrow
// for the longest label present (e.g. "Unprivileged" wrapping to "Unprivilege /
// d" for LXC). Every label must appear intact on a single line.
func TestRenderResultLabelsNoWrap(t *testing.T) {
	cases := []struct {
		name   string
		vm     *state.VMSpec
		labels []string
	}{
		{
			name: "VM",
			vm: &state.VMSpec{
				Name: "web-01", VMID: 6100, Node: "pve1", Plan: "KVM2",
				Cores: 2, MemoryGB: 4, DiskGB: 32, Username: "admin",
				Software: []string{"docker", "node"},
			},
			labels: []string{"Name", "ID / Node", "Plan", "CPU / RAM", "Disk", "IP", "User", "Login", "Software"},
		},
		{
			name: "LXC",
			vm: &state.VMSpec{
				Type: "lxc", Name: "dns-01", VMID: 6101, Node: "pve2",
				Cores: 1, MemoryMB: 512, DiskGB: 8, Username: "root",
				Unprivileged: true, Nesting: true,
			},
			labels: []string{"Name", "ID / Node", "CPU / RAM", "Disk", "IP", "User", "Login", "Unprivileged", "Nesting"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderResult("✓ created", tc.vm, "192.168.1.50")
			for _, label := range tc.labels {
				if !strings.Contains(out, label) {
					t.Errorf("label %q not rendered intact (wrapped?) in:\n%s", label, out)
				}
			}
		})
	}
}

func TestPickNode(t *testing.T) {
	tests := []struct {
		name        string
		explicit    string
		defaultNode string
		candidates  []string
		want        string
		wantErr     bool
	}{
		{"explicit wins", "pve2", "pve1", []string{"pve1", "pve2"}, "pve2", false},
		{"explicit not a candidate errors", "mars", "pve1", []string{"pve1", "pve2"}, "", true},
		{"default_node wins over discovery order", "", "pve1", []string{"pve2", "pve1"}, "pve1", false},
		{"default_node not a candidate falls back to first", "", "mars", []string{"pve2", "pve1"}, "pve2", false},
		{"no explicit, no default falls back to first", "", "", []string{"pve2", "pve1"}, "pve2", false},
		{"single candidate", "", "", []string{"pve1"}, "pve1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickNode(tt.explicit, tt.defaultNode, tt.candidates)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("pickNode() expected error, got node %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pickNode() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("pickNode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIPOrDash(t *testing.T) {
	if got := ipOrDash("192.168.1.50"); got != "192.168.1.50" {
		t.Errorf("ipOrDash(ip) = %q, want the ip unchanged", got)
	}
	if got := ipOrDash(""); got == "" {
		t.Error("ipOrDash(\"\") should return a placeholder, not empty string")
	}
}
