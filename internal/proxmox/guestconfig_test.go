package proxmox

import (
	"reflect"
	"testing"
)

func TestCfgStr(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{"string value", map[string]any{"name": "web-01"}, "name", "web-01"},
		{"numeric value (PVE sometimes returns numbers as float64)", map[string]any{"template": float64(1)}, "template", "1"},
		{"missing key", map[string]any{}, "name", ""},
		{"wrong type (bool) returns empty", map[string]any{"x": true}, "x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfgStr(tt.m, tt.key); got != tt.want {
				t.Errorf("cfgStr(%v, %q) = %q, want %q", tt.m, tt.key, got, tt.want)
			}
		})
	}
}

func TestCfgInt(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{"numeric value", map[string]any{"cores": float64(4)}, "cores", 4},
		{"string value", map[string]any{"cores": "4"}, "cores", 4},
		{"string value with whitespace", map[string]any{"cores": " 4 "}, "cores", 4},
		{"missing key", map[string]any{}, "cores", 0},
		{"unparseable string", map[string]any{"cores": "abc"}, "cores", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfgInt(tt.m, tt.key); got != tt.want {
				t.Errorf("cfgInt(%v, %q) = %d, want %d", tt.m, tt.key, got, tt.want)
			}
		})
	}
}

func TestDiskStorage(t *testing.T) {
	tests := []struct{ spec, want string }{
		{"local-lvm:base-200-disk-0,iothread=1,size=32256M", "local-lvm"},
		{"no-colon-here", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := diskStorage(tt.spec); got != tt.want {
			t.Errorf("diskStorage(%q) = %q, want %q", tt.spec, got, tt.want)
		}
	}
}

func TestNetField(t *testing.T) {
	spec := "virtio=AA:BB,bridge=vmbr0,firewall=1"
	tests := []struct{ key, want string }{
		{"bridge", "vmbr0"},
		{"firewall", "1"},
		{"virtio", "AA:BB"},
		{"missing", ""},
	}
	for _, tt := range tests {
		if got := netField(spec, tt.key); got != tt.want {
			t.Errorf("netField(%q, %q) = %q, want %q", spec, tt.key, got, tt.want)
		}
	}
}

func TestParseIPConfig(t *testing.T) {
	tests := []struct {
		name        string
		spec        string
		wantDHCP    bool
		wantCIDR    string
		wantGateway string
	}{
		{"explicit dhcp", "ip=dhcp", true, "", ""},
		{"missing ip field entirely", "gw=192.168.1.1", true, "", ""},
		{"static", "ip=192.168.1.50/24,gw=192.168.1.1", false, "192.168.1.50/24", "192.168.1.1"},
		{"static without gateway", "ip=192.168.1.50/24", false, "192.168.1.50/24", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dhcp, cidr, gw := parseIPConfig(tt.spec)
			if dhcp != tt.wantDHCP || cidr != tt.wantCIDR || gw != tt.wantGateway {
				t.Errorf("parseIPConfig(%q) = (%v, %q, %q), want (%v, %q, %q)",
					tt.spec, dhcp, cidr, gw, tt.wantDHCP, tt.wantCIDR, tt.wantGateway)
			}
		})
	}
}

func TestAgentEnabled(t *testing.T) {
	tests := []struct {
		spec string
		want bool
	}{
		{"1", true},
		{"0", false},
		{"1,fstrim_cloned_disks=1", true},
		{"0,fstrim_cloned_disks=1", false},
		{"enabled=1", true},
		{"enabled=0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := agentEnabled(tt.spec); got != tt.want {
			t.Errorf("agentEnabled(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}

func TestFeatureFlag(t *testing.T) {
	tests := []struct {
		spec, key string
		want      bool
	}{
		{"nesting=1,keyctl=1", "nesting", true},
		{"nesting=0,keyctl=1", "nesting", false},
		{"nesting=0", "nesting", false},
		{"", "nesting", false},
	}
	for _, tt := range tests {
		if got := featureFlag(tt.spec, tt.key); got != tt.want {
			t.Errorf("featureFlag(%q, %q) = %v, want %v", tt.spec, tt.key, got, tt.want)
		}
	}
	// keyctl specifically:
	if !featureFlag("nesting=0,keyctl=1", "keyctl") {
		t.Error("featureFlag(nesting=0,keyctl=1, keyctl) should be true")
	}
}

func TestParseSSHKeys(t *testing.T) {
	// URL-encoded, %0A-separated. Deliberately includes a '+' which must
	// survive PathUnescape (QueryUnescape would corrupt it into a space).
	encoded := "ssh-rsa%20AAAAB3NzaC1yc2EAAAADAQABAAAB%2Bxyz%20user%40host%0Assh-ed25519%20AAAAC3%20user2%40host"
	got := parseSSHKeys(encoded)
	want := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAB+xyz user@host",
		"ssh-ed25519 AAAAC3 user2@host",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSSHKeys(%q) = %v, want %v", encoded, got, want)
	}
}

func TestParseSSHKeysEmptyLinesSkipped(t *testing.T) {
	got := parseSSHKeys("key-one%0A%0Akey-two")
	want := []string{"key-one", "key-two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSSHKeys(with blank line) = %v, want %v", got, want)
	}
}

func TestParseSSHKeysInvalidEscapeFallsBackToRaw(t *testing.T) {
	// An invalid % escape should not error out silently losing the key; it
	// falls back to the raw string.
	got := parseSSHKeys("not%zzvalid")
	if len(got) != 1 || got[0] != "not%zzvalid" {
		t.Errorf("parseSSHKeys(invalid escape) = %v, want the raw string preserved", got)
	}
}

func TestIsDiskKeyIsNetKeyIsMountPointKey(t *testing.T) {
	diskTests := map[string]bool{
		"scsi0": true, "scsi15": true, "virtio0": true, "sata0": true, "ide2": true,
		"net0": false, "mp0": false, "scsi": false, "rootfs": false,
	}
	for k, want := range diskTests {
		if got := isDiskKey(k); got != want {
			t.Errorf("isDiskKey(%q) = %v, want %v", k, got, want)
		}
	}

	netTests := map[string]bool{
		"net0": true, "net1": true, "net10": true, "scsi0": false, "network": false,
	}
	for k, want := range netTests {
		if got := isNetKey(k); got != want {
			t.Errorf("isNetKey(%q) = %v, want %v", k, got, want)
		}
	}

	mpTests := map[string]bool{
		"mp0": true, "mp1": true, "mp": false, "map0": false,
	}
	for k, want := range mpTests {
		if got := isMountPointKey(k); got != want {
			t.Errorf("isMountPointKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestExtraNICKeys(t *testing.T) {
	cfg := map[string]any{
		"net0":  "virtio=AA,bridge=vmbr0",
		"net2":  "virtio=BB,bridge=vmbr0",
		"net1":  "virtio=CC,bridge=vmbr0",
		"scsi0": "local-lvm:vm-100-disk-0",
	}
	got := extraNICKeys(cfg)
	want := []string{"net1", "net2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extraNICKeys() = %v, want %v (net0 excluded, sorted)", got, want)
	}
}

func TestExtraNICKeysNoneWhenOnlyNet0(t *testing.T) {
	cfg := map[string]any{"net0": "virtio=AA,bridge=vmbr0"}
	got := extraNICKeys(cfg)
	if len(got) != 0 {
		t.Errorf("extraNICKeys() = %v, want empty", got)
	}
}

func TestBootDiskSpec(t *testing.T) {
	t.Run("prefers boot order", func(t *testing.T) {
		cfg := map[string]any{
			"boot":    "order=virtio0;ide2",
			"virtio0": "local-lvm:vm-100-disk-0,size=32G",
			"scsi0":   "local-lvm:vm-100-disk-1,size=64G", // should be ignored; not in boot order
		}
		iface, spec := bootDiskSpec(cfg)
		if iface != "virtio0" || spec != "local-lvm:vm-100-disk-0,size=32G" {
			t.Errorf("bootDiskSpec() = (%q, %q), want (virtio0, the virtio0 spec)", iface, spec)
		}
	})

	t.Run("falls back to conventional names without boot order", func(t *testing.T) {
		cfg := map[string]any{
			"scsi0": "local-lvm:vm-100-disk-0,size=32G",
		}
		iface, spec := bootDiskSpec(cfg)
		if iface != "scsi0" || spec != "local-lvm:vm-100-disk-0,size=32G" {
			t.Errorf("bootDiskSpec() = (%q, %q), want (scsi0, the scsi0 spec)", iface, spec)
		}
	})

	t.Run("skips cloud-init drive", func(t *testing.T) {
		cfg := map[string]any{
			"boot":  "order=scsi0;scsi1",
			"scsi0": "local-lvm:vm-100-cloudinit",
			"scsi1": "local-lvm:vm-100-disk-0,size=32G",
		}
		iface, spec := bootDiskSpec(cfg)
		if iface != "scsi1" || spec != "local-lvm:vm-100-disk-0,size=32G" {
			t.Errorf("bootDiskSpec() = (%q, %q), want (scsi1, the real disk)", iface, spec)
		}
	})

	t.Run("no boot disk found", func(t *testing.T) {
		iface, spec := bootDiskSpec(map[string]any{})
		if iface != "" || spec != "" {
			t.Errorf("bootDiskSpec(empty) = (%q, %q), want (\"\", \"\")", iface, spec)
		}
	})
}

func TestParseVMConfig(t *testing.T) {
	cfg := map[string]any{
		"name":       "web-01",
		"cores":      float64(4),
		"sockets":    float64(1),
		"memory":     float64(4096),
		"ciuser":     "admin",
		"template":   float64(0),
		"scsi0":      "local-lvm:vm-100-disk-0,size=32768M",
		"net0":       "virtio=AA:BB,bridge=vmbr0",
		"agent":      "1",
		"ipconfig0":  "ip=192.168.1.50/24,gw=192.168.1.1",
		"nameserver": "192.168.1.1 1.1.1.1",
		"sshkeys":    "ssh-ed25519%20AAAA%20user%40host",
		"scsi1":      "local-lvm:vm-100-disk-1,size=10G", // extra disk
		"net1":       "virtio=CC,bridge=vmbr1",           // extra NIC
	}
	g := parseVMConfig(cfg)

	if g.Name != "web-01" || g.Cores != 4 || g.Sockets != 1 || g.MemoryMB != 4096 {
		t.Errorf("basic fields mismatch: %+v", g)
	}
	if g.CIUser != "admin" {
		t.Errorf("CIUser = %q, want admin", g.CIUser)
	}
	if g.Template {
		t.Errorf("Template should be false (template=0)")
	}
	if g.BootDiskIface != "scsi0" || g.DiskGB != 32 || g.Storage != "local-lvm" {
		t.Errorf("boot disk mismatch: iface=%q diskGB=%d storage=%q", g.BootDiskIface, g.DiskGB, g.Storage)
	}
	if g.Bridge != "vmbr0" {
		t.Errorf("Bridge = %q, want vmbr0", g.Bridge)
	}
	if !g.AgentEnabled {
		t.Error("AgentEnabled should be true")
	}
	if g.DHCP || g.IPCIDR != "192.168.1.50/24" || g.Gateway != "192.168.1.1" {
		t.Errorf("IP config mismatch: DHCP=%v CIDR=%q GW=%q", g.DHCP, g.IPCIDR, g.Gateway)
	}
	if !reflect.DeepEqual(g.DNS, []string{"192.168.1.1", "1.1.1.1"}) {
		t.Errorf("DNS = %v, want [192.168.1.1 1.1.1.1]", g.DNS)
	}
	if len(g.SSHKeys) != 1 || g.SSHKeys[0] != "ssh-ed25519 AAAA user@host" {
		t.Errorf("SSHKeys = %v", g.SSHKeys)
	}
	if !reflect.DeepEqual(g.ExtraDisks, []string{"scsi1"}) {
		t.Errorf("ExtraDisks = %v, want [scsi1]", g.ExtraDisks)
	}
	if !reflect.DeepEqual(g.ExtraNICs, []string{"net1"}) {
		t.Errorf("ExtraNICs = %v, want [net1]", g.ExtraNICs)
	}
}

func TestParseVMConfigDefaultSocketsAndDHCPFallback(t *testing.T) {
	cfg := map[string]any{
		"name":   "web-02",
		"cores":  float64(2),
		"memory": float64(2048),
		// no "sockets" key, no "ipconfig0" key at all
	}
	g := parseVMConfig(cfg)
	if g.Sockets != 1 {
		t.Errorf("Sockets should default to 1 when unset, got %d", g.Sockets)
	}
	if !g.DHCP {
		t.Error("DHCP should be true when ipconfig0 is entirely absent")
	}
}

func TestParseVMConfigExcludesCloudInitDiskFromExtraDisks(t *testing.T) {
	cfg := map[string]any{
		"scsi0": "local-lvm:vm-100-disk-0,size=32G",
		"scsi1": "local-lvm:vm-100-cloudinit", // must NOT be reported as an extra disk
	}
	g := parseVMConfig(cfg)
	if len(g.ExtraDisks) != 0 {
		t.Errorf("ExtraDisks = %v, want empty (cloud-init drive excluded)", g.ExtraDisks)
	}
}

func TestParseContainerConfig(t *testing.T) {
	cfg := map[string]any{
		"hostname":     "ct-01",
		"cores":        float64(2),
		"memory":       float64(1024),
		"swap":         float64(512),
		"ostype":       "debian",
		"unprivileged": float64(1),
		"template":     float64(0),
		"rootfs":       "local-lvm:vm-101-disk-0,size=8G",
		"net0":         "name=eth0,bridge=vmbr0,ip=192.168.1.101/24,gw=192.168.1.1",
		"nameserver":   "192.168.1.1",
		"features":     "nesting=1,keyctl=1",
		"mp0":          "local-lvm:vm-101-disk-1,mp=/data",
		"net1":         "name=eth1,bridge=vmbr1",
	}
	g := parseContainerConfig(cfg)

	if g.Name != "ct-01" || g.Cores != 2 || g.MemoryMB != 1024 || g.SwapMB != 512 {
		t.Errorf("basic fields mismatch: %+v", g)
	}
	if g.OSType != "debian" || !g.Unprivileged {
		t.Errorf("OSType/Unprivileged mismatch: %+v", g)
	}
	if g.DiskGB != 8 || g.Storage != "local-lvm" || g.BootDiskIface != "rootfs" {
		t.Errorf("rootfs mismatch: %+v", g)
	}
	if g.Bridge != "vmbr0" {
		t.Errorf("Bridge = %q, want vmbr0", g.Bridge)
	}
	if g.DHCP || g.IPCIDR != "192.168.1.101/24" || g.Gateway != "192.168.1.1" {
		t.Errorf("IP config mismatch: %+v", g)
	}
	if !reflect.DeepEqual(g.DNS, []string{"192.168.1.1"}) {
		t.Errorf("DNS = %v", g.DNS)
	}
	if !g.Nesting {
		t.Error("Nesting should be true (features=nesting=1,keyctl=1)")
	}
	if !reflect.DeepEqual(g.MountPoints, []string{"mp0"}) {
		t.Errorf("MountPoints = %v, want [mp0]", g.MountPoints)
	}
	if !reflect.DeepEqual(g.ExtraNICs, []string{"net1"}) {
		t.Errorf("ExtraNICs = %v, want [net1]", g.ExtraNICs)
	}
}

func TestParseContainerConfigHostManaged(t *testing.T) {
	on := parseContainerConfig(map[string]any{
		"hostname": "ct",
		"net0":     "name=eth0,bridge=vmbr0,ip=192.168.1.101/24,gw=192.168.1.1,host-managed=1",
	})
	if !on.HostManaged {
		t.Error("host-managed=1 should parse as HostManaged true")
	}
	off := parseContainerConfig(map[string]any{
		"hostname": "ct",
		"net0":     "name=eth0,bridge=vmbr0,ip=192.168.1.101/24,gw=192.168.1.1,host-managed=0",
	})
	if off.HostManaged {
		t.Error("host-managed=0 should parse as HostManaged false")
	}
	absent := parseContainerConfig(map[string]any{
		"hostname": "ct",
		"net0":     "name=eth0,bridge=vmbr0",
	})
	if absent.HostManaged {
		t.Error("no host-managed field should parse as HostManaged false")
	}
}

func TestParseContainerConfigNoNet0IsDHCP(t *testing.T) {
	g := parseContainerConfig(map[string]any{"hostname": "ct-02"})
	if !g.DHCP {
		t.Error("DHCP should default to true when net0 is entirely absent")
	}
}
