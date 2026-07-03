package proxmox

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVersion drives the /version endpoint against a fake API and confirms the
// version string is parsed out (it gates PVE 9.1+ host-managed networking).
func TestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/version") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"data":{"version":"9.1.2","release":"9.1"}}`))
	}))
	defer srv.Close()

	v, err := New(srv.URL, "t", "s", false).Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "9.1.2" {
		t.Errorf("Version() = %q, want 9.1.2", v)
	}
}

// TestVersionError surfaces a transport/HTTP failure rather than returning a bogus
// version string.
func TestVersionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "t", "s", false).Version(); err == nil {
		t.Fatal("Version() should surface a 500 as an error")
	}
}

func TestVolidBase(t *testing.T) {
	tests := []struct{ volid, want string }{
		{"local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst", "debian-12-standard_12.7-1_amd64.tar.zst"},
		{"no-slash-here", "no-slash-here"},
		{"", ""},
		{"a/b/c", "c"},
	}
	for _, tt := range tests {
		if got := volidBase(tt.volid); got != tt.want {
			t.Errorf("volidBase(%q) = %q, want %q", tt.volid, got, tt.want)
		}
	}
}

func TestOSTypeFromTemplate(t *testing.T) {
	tests := []struct{ name, want string }{
		{"local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst", "debian"},
		{"local:vztmpl/ubuntu-24.04-standard_24.04-1_amd64.tar.zst", "ubuntu"},
		{"local:vztmpl/alpine-3.19-default_20240207_amd64.tar.xz", "alpine"},
		{"local:vztmpl/centos-9-stream_amd64.tar.zst", "centos"},
		{"local:vztmpl/fedora-39-default_amd64.tar.xz", "fedora"},
		{"local:vztmpl/rockylinux-9-default_amd64.tar.xz", "rockylinux"},
		{"local:vztmpl/archlinux-base_amd64.tar.zst", "archlinux"},
		{"local:vztmpl/opensuse-15-default_amd64.tar.xz", "opensuse"},
		{"local:vztmpl/devuan-5-standard_amd64.tar.zst", "devuan"},
		{"local:vztmpl/gentoo-openrc-amd64.tar.xz", "gentoo"},
		{"local:vztmpl/nixos-23.11-amd64.tar.xz", "nixos"},
		{"UBUNTU-UPPERCASE_amd64.tar.zst", "ubuntu"},                 // case-insensitive
		{"local:vztmpl/some-unknown-distro_amd64.tar.zst", "debian"}, // defaults to debian
		{"", "debian"},
	}
	for _, tt := range tests {
		if got := OSTypeFromTemplate(tt.name); got != tt.want {
			t.Errorf("OSTypeFromTemplate(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSizeToGB(t *testing.T) {
	tests := []struct {
		spec string
		want int
	}{
		{"32256M", 32}, // rounds up: 32256/1024 = 31.5 -> 32
		{"32G", 32},
		{"1T", 1024},
		{"1048576K", 1}, // 1 GiB in KiB
		{"", 0},
		{"garbage", 0},
		{"10", 10}, // no unit suffix — treated as GB (mult stays 1.0)
	}
	for _, tt := range tests {
		if got := sizeToGB(tt.spec); got != tt.want {
			t.Errorf("sizeToGB(%q) = %d, want %d", tt.spec, got, tt.want)
		}
	}
}

func TestDiskSpecGB(t *testing.T) {
	tests := []struct {
		spec string
		want int
	}{
		{"local-lvm:base-200-disk-0,iothread=1,size=32256M", 32},
		{"local-lvm:vm-100-disk-0,size=64G", 64},
		{"local-lvm:vm-100-disk-0", 0}, // no size= field
		{"", 0},
	}
	for _, tt := range tests {
		if got := diskSpecGB(tt.spec); got != tt.want {
			t.Errorf("diskSpecGB(%q) = %d, want %d", tt.spec, got, tt.want)
		}
	}
}

func TestParseClusterMetrics(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	rows := []resourceRow{
		{Type: "node", Node: "pve2", Status: "online", CPU: 0.12, Mem: 7 * gib, MaxMem: 16 * gib, Disk: 30 * gib, MaxDisk: 200 * gib, Uptime: 3600},
		{Type: "node", Node: "pve1", Status: "online", CPU: 0.34, Mem: 11 * gib, MaxMem: 16 * gib},
		{Type: "storage", Storage: "local-lvm", Node: "pve1", Status: "available", Disk: 62 * gib, MaxDisk: 100 * gib, Plugin: "lvmthin", Content: "images", Shared: 0},
		{Type: "qemu", Node: "pve1"}, // guest rows are ignored
		{Type: "sdn", Node: "pve1"},  // sdn rows are ignored
	}
	d := parseClusterMetrics(rows)

	if len(d.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(d.Nodes))
	}
	// Sorted by name: pve1 before pve2.
	if d.Nodes[0].Name != "pve1" || d.Nodes[1].Name != "pve2" {
		t.Errorf("nodes not sorted by name: %q, %q", d.Nodes[0].Name, d.Nodes[1].Name)
	}
	j := d.Nodes[0]
	if j.MemUsedMB != 11*1024 || j.MemMaxMB != 16*1024 {
		t.Errorf("pve1 mem = %d/%d MB, want 11264/16384", j.MemUsedMB, j.MemMaxMB)
	}
	p := d.Nodes[1]
	if p.DiskUsedGB != 30 || p.DiskMaxGB != 200 {
		t.Errorf("pve2 disk = %d/%d GB, want 30/200", p.DiskUsedGB, p.DiskMaxGB)
	}
	if p.CPUFrac != 0.12 || p.Uptime != 3600 {
		t.Errorf("pve2 cpu/uptime = %v/%d, want 0.12/3600", p.CPUFrac, p.Uptime)
	}

	if len(d.Storage) != 1 {
		t.Fatalf("storage = %d, want 1", len(d.Storage))
	}
	s := d.Storage[0]
	if s.Name != "local-lvm" || s.UsedGB != 62 || s.TotalGB != 100 || s.Shared {
		t.Errorf("storage = %+v, want local-lvm 62/100 shared=false", s)
	}
}
