package cmd

import (
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/state"
)

func TestVolidDisplay(t *testing.T) {
	tests := []struct{ volid, want string }{
		{"local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst", "debian-12-standard_12.7-1_amd64.tar.zst"},
		{"no-slash-here", "no-slash-here"},
		{"a/b/c", "c"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := volidDisplay(tt.volid); got != tt.want {
			t.Errorf("volidDisplay(%q) = %q, want %q", tt.volid, got, tt.want)
		}
	}
}

func TestYesNo(t *testing.T) {
	if got := yesNo(true); got != "yes" {
		t.Errorf("yesNo(true) = %q, want yes", got)
	}
	if got := yesNo(false); got != "no" {
		t.Errorf("yesNo(false) = %q, want no", got)
	}
}

func TestProvisionedDesc(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"no software shows a dash", &state.VMSpec{}, "-"},
		{"single item", &state.VMSpec{Software: []string{"docker"}}, "docker"},
		{"comma-joined, no spaces", &state.VMSpec{Software: []string{"docker", "node"}}, "docker,node"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provisionedDesc(tt.vm); got != tt.want {
				t.Errorf("provisionedDesc() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoginDesc(t *testing.T) {
	tests := []struct {
		name string
		vm   *state.VMSpec
		want string
	}{
		{"key only", &state.VMSpec{SSHKeys: []string{"ssh-ed25519 AAAA"}}, "ssh key"},
		{"password only", &state.VMSpec{HasPassword: true}, "password"},
		{"both", &state.VMSpec{HasPassword: true, SSHKeys: []string{"ssh-ed25519 AAAA"}}, "password + ssh key"},
		{"neither still reports ssh key", &state.VMSpec{}, "ssh key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := loginDesc(tt.vm); got != tt.want {
				t.Errorf("loginDesc() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateOptionalCIDR(t *testing.T) {
	valid := []string{"", "1", "24", "32"}
	for _, s := range valid {
		if err := validateOptionalCIDR(s); err != nil {
			t.Errorf("validateOptionalCIDR(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"0", "33", "-1", "abc", "24.5"}
	for _, s := range invalid {
		if err := validateOptionalCIDR(s); err == nil {
			t.Errorf("validateOptionalCIDR(%q) = nil, want an error", s)
		}
	}
}

func TestParseCIDROr(t *testing.T) {
	tests := []struct {
		in   string
		def  int
		want int
	}{
		{"24", 16, 24},
		{"1", 16, 1},
		{"32", 16, 32},
		{"0", 16, 16},   // out of range → default
		{"33", 16, 16},  // out of range → default
		{"", 24, 24},    // empty → default
		{"abc", 24, 24}, // unparseable → default
	}
	for _, tt := range tests {
		if got := parseCIDROr(tt.in, tt.def); got != tt.want {
			t.Errorf("parseCIDROr(%q, %d) = %d, want %d", tt.in, tt.def, got, tt.want)
		}
	}
}

func TestVersionString(t *testing.T) {
	// versionString is "hlab " + fullVersion(); fullVersion starts with Version.
	vs := versionString()
	if !strings.HasPrefix(vs, "hlab "+Version) {
		t.Errorf("versionString() = %q, want it to start with %q", vs, "hlab "+Version)
	}
	if !strings.HasPrefix(fullVersion(), Version) {
		t.Errorf("fullVersion() = %q, want it to start with the Version %q", fullVersion(), Version)
	}
}
