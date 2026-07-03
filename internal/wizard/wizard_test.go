package wizard

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/plans"
	"github.com/aikssen/hlab/internal/state"
)

func TestDefaultPlanName(t *testing.T) {
	tests := []struct {
		name string
		ps   []plans.Plan
		want string
	}{
		{"KVM2 present is preferred", []plans.Plan{{Name: "KVM1"}, {Name: "KVM2"}, {Name: "KVM4"}}, "KVM2"},
		{"no KVM2, falls back to first", []plans.Plan{{Name: "KVM1"}, {Name: "KVM4"}}, "KVM1"},
		{"empty list falls back to Custom", nil, plans.Custom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultPlanName(tt.ps); got != tt.want {
				t.Errorf("defaultPlanName(%v) = %q, want %q", tt.ps, got, tt.want)
			}
		})
	}
}

func TestDefaultLXCPlanName(t *testing.T) {
	tests := []struct {
		name string
		ps   []plans.Plan
		want string
	}{
		{"micro present is preferred", []plans.Plan{{Name: "small"}, {Name: "micro"}}, "micro"},
		{"no micro, falls back to first", []plans.Plan{{Name: "small"}, {Name: "large"}}, "small"},
		{"empty list falls back to Custom", nil, plans.Custom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultLXCPlanName(tt.ps); got != tt.want {
				t.Errorf("defaultLXCPlanName(%v) = %q, want %q", tt.ps, got, tt.want)
			}
		})
	}
}

func TestValidateNonEmpty(t *testing.T) {
	if err := validateNonEmpty("hello"); err != nil {
		t.Errorf("validateNonEmpty(hello) = %v, want nil", err)
	}
	if err := validateNonEmpty(""); err == nil {
		t.Error("validateNonEmpty(\"\") should error")
	}
	if err := validateNonEmpty("   "); err == nil {
		t.Error("validateNonEmpty(whitespace only) should error")
	}
}

func TestValidatePositiveInt(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"1", false},
		{"100", false},
		{"0", true},
		{"-1", true},
		{"abc", true},
		{"", true},
		{"1.5", true},
	}
	for _, tt := range tests {
		err := validatePositiveInt(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validatePositiveInt(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
	}
}

func TestValidateNonNegativeInt(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"0", false},
		{"5", false},
		{"-1", true},
		{"abc", true},
	}
	for _, tt := range tests {
		err := validateNonNegativeInt(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateNonNegativeInt(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
	}
}

func TestValidateMemMB(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"512", false}, // bare number is GB per plans.ParseMem
		{"512M", false},
		{"2G", false},
		{"0", true},
		{"", true},
		{"abc", true},
	}
	for _, tt := range tests {
		err := validateMemMB(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateMemMB(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
	}
}

func TestValidateHostname(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"web-01", false},
		{"web01", false},
		{"", true},
		{"   ", true},
		{"Web-01", true},      // uppercase rejected
		{"web_01", true},      // underscore rejected
		{"web.example", true}, // dot rejected
		{"web 01", true},      // space rejected
	}
	for _, tt := range tests {
		err := validateHostname(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateHostname(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
	}
}

func TestAtoi(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"42", 42},
		{" 42 ", 42},
		{"abc", 0},
		{"", 0},
		{"-5", -5},
	}
	for _, tt := range tests {
		if got := atoi(tt.in); got != tt.want {
			t.Errorf("atoi(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"192.168.1.1,1.1.1.1", []string{"192.168.1.1", "1.1.1.1"}},
		{"192.168.1.1, 1.1.1.1", []string{"192.168.1.1", "1.1.1.1"}},
		{"", nil},
		{"   ", nil},
		{"single", []string{"single"}},
		{"a,,b", []string{"a", "b"}}, // empty fields between commas are dropped
	}
	for _, tt := range tests {
		got := splitCSV(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSummary(t *testing.T) {
	vm := &state.VMSpec{
		Node:       "pve1",
		Template:   "ubuntu-26.04-v2",
		TemplateID: 100,
		Storage:    "local-lvm",
		VMID:       6100,
		Name:       "web-01",
		Cores:      2,
		MemoryGB:   4,
		DiskGB:     32,
		DHCP:       true,
		Username:   "admin",
	}
	out := summary(vm, "")
	for _, want := range []string{"pve1", "ubuntu-26.04-v2", "6100", "web-01", "2 cores", "4 GB", "32 GB", "DHCP", "admin", "ssh key"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary() missing %q in:\n%s", want, out)
		}
	}
}

func TestSummaryReportsStaticNetworkAndPassword(t *testing.T) {
	vm := &state.VMSpec{
		DHCP:    false,
		IPCIDR:  "192.168.1.50/24",
		Gateway: "192.168.1.1",
		SSHKeys: []string{"ssh-ed25519 AAAA"},
	}
	out := summary(vm, "s3cr3t")
	if !strings.Contains(out, "192.168.1.50/24 gw 192.168.1.1") {
		t.Errorf("summary() should show the static network, got:\n%s", out)
	}
	if !strings.Contains(out, "password + ssh key") {
		t.Errorf("summary() should report both login methods when both are set, got:\n%s", out)
	}
}

func TestSummaryCT(t *testing.T) {
	vm := &state.VMSpec{
		Node:         "pve2",
		Template:     "debian-12-standard",
		Storage:      "local",
		VMID:         6101,
		Name:         "ct-01",
		Cores:        1,
		MemoryMB:     512,
		DiskGB:       4,
		Unprivileged: true,
		DHCP:         true,
	}
	out := summaryCT(vm, "")
	// Nesting is always on for hlab-created containers, so the summary shows a
	// fixed "on" regardless of the spec value.
	for _, want := range []string{"pve2", "debian-12-standard", "6101", "ct-01", "1 cores", "512 MB", "4 GB", "true", "Nesting:      on", "DHCP", "root", "ssh key"} {
		if !strings.Contains(out, want) {
			t.Errorf("summaryCT() missing %q in:\n%s", want, out)
		}
	}
}

func TestSummaryCTPasswordOnly(t *testing.T) {
	vm := &state.VMSpec{DHCP: true}
	out := summaryCT(vm, "s3cr3t")
	if !strings.Contains(out, "root (password)") {
		t.Errorf("summaryCT() should report password-only login, got:\n%s", out)
	}
}
