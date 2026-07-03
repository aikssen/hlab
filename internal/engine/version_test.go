package engine

import (
	"testing"

	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/wizard"
)

func TestPVEVersionAtLeast(t *testing.T) {
	cases := []struct {
		version      string
		major, minor int
		want         bool
	}{
		{"9.1.2", 9, 1, true},  // patch suffix ignored, 9.1 >= 9.1
		{"9.0-4", 9, 1, false}, // build suffix on minor, 9.0 < 9.1
		{"8.4.1", 9, 1, false}, // older major
		{"10.0", 9, 1, true},   // newer major, minor is lower
		{"9.1", 9, 1, true},    // exact
		{"9.2", 9, 1, true},    // same major, higher minor
		{"", 9, 1, false},      // unparseable → fail closed
		{"garbage", 9, 1, false},
	}
	for _, c := range cases {
		if got := pveVersionAtLeast(c.version, c.major, c.minor); got != c.want {
			t.Errorf("pveVersionAtLeast(%q, %d, %d) = %v, want %v", c.version, c.major, c.minor, got, c.want)
		}
	}
}

// TestCreateSetsHostManagedForStaticLXC verifies the Create version gate: a
// static-IP container gets HostManagedNet only on PVE 9.1+; a DHCP container or
// an older Proxmox never does.
func TestCreateSetsHostManagedForStaticLXC(t *testing.T) {
	newStaticCT := func() *state.VMSpec {
		return &state.VMSpec{Name: "ct", VMID: 6101, Type: "lxc", DHCP: false, IPCIDR: "192.168.1.101/24"}
	}
	cases := []struct {
		name    string
		version string
		vm      *state.VMSpec
		want    bool
	}{
		{"static-ct-on-9.1", "9.1.2", newStaticCT(), true},
		{"static-ct-on-8.4", "8.4.1", newStaticCT(), false},
		{"dhcp-ct-on-9.1", "9.1.2", &state.VMSpec{Name: "ct", VMID: 6101, Type: "lxc", DHCP: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{version: c.version})
			if _, err := e.Create(&wizard.Result{VM: c.vm}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if c.vm.HostManagedNet != c.want {
				t.Errorf("HostManagedNet = %v, want %v", c.vm.HostManagedNet, c.want)
			}
		})
	}
}
