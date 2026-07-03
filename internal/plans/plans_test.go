package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMem(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"bare integer is GB", "2", 2048, false},
		{"bare float is GB", "0.5", 512, false},
		{"explicit M suffix", "512M", 512, false},
		{"explicit MB suffix", "512MB", 512, false},
		{"explicit G suffix", "2G", 2048, false},
		{"explicit GB suffix", "2GB", 2048, false},
		{"uppercase input", "512M", 512, false},
		{"mixed case suffix", "2Gb", 2048, false},
		{"leading/trailing space", "  2  ", 2048, false},
		{"space before suffix", " 512 M ", 512, false},
		{"empty string errors", "", 0, true},
		{"only whitespace errors", "   ", 0, true},
		{"zero errors", "0", 0, true},
		{"negative errors", "-1", 0, true},
		{"non-numeric errors", "abc", 0, true},
		{"non-numeric with suffix errors", "abcM", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMem(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMem(%q) = %d, nil; want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMem(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseMem(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatMem(t *testing.T) {
	tests := []struct {
		mb   int
		want string
	}{
		{2048, "2"},
		{1024, "1"},
		{512, "512M"},
		{2560, "2560M"},
		{0, "0M"}, // 0 % 1024 == 0 is true, but "mb > 0" guard fails -> falls to the M branch
	}
	for _, tt := range tests {
		got := FormatMem(tt.mb)
		if got != tt.want {
			t.Errorf("FormatMem(%d) = %q, want %q", tt.mb, got, tt.want)
		}
	}
}

func TestParseMemFormatMemRoundTrip(t *testing.T) {
	// Property: for whole-GB and explicit-MB values, ParseMem(FormatMem(mb)) == mb.
	for _, mb := range []int{1024, 2048, 4096, 8192, 512, 2560, 768} {
		s := FormatMem(mb)
		got, err := ParseMem(s)
		if err != nil {
			t.Fatalf("ParseMem(FormatMem(%d)=%q) errored: %v", mb, s, err)
		}
		if got != mb {
			t.Errorf("round trip mismatch: FormatMem(%d) = %q, ParseMem(...) = %d", mb, s, got)
		}
	}
}

func TestPlanMB(t *testing.T) {
	tests := []struct {
		name string
		p    Plan
		want int
	}{
		{"MemoryMB set wins", Plan{MemoryGB: 4, MemoryMB: 512}, 512},
		{"falls back to MemoryGB*1024", Plan{MemoryGB: 4}, 4096},
		{"zero everything", Plan{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.MB(); got != tt.want {
				t.Errorf("Plan.MB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPlanDisplayLabel(t *testing.T) {
	tests := []struct {
		name string
		p    Plan
		want string
	}{
		{
			"explicit label wins",
			Plan{Label: "Custom Label", Name: "KVM2", Cores: 2, MemoryGB: 4, DiskGB: 32},
			"Custom Label",
		},
		{
			"derived label, whole-GB VM plan",
			Plan{Name: "KVM2", Cores: 2, MemoryGB: 4, DiskGB: 32},
			"KVM2 — 2c · 4GB · 32GB",
		},
		{
			"derived label, sub-GB LXC tier",
			Plan{Name: "micro", Cores: 1, MemoryMB: 512, DiskGB: 4},
			"micro — 1c · 512MB · 4GB",
		},
		{
			"derived label, whole-GB LXC tier via MemoryMB",
			Plan{Name: "large", Cores: 4, MemoryMB: 4096, DiskGB: 64},
			"large — 4c · 4GB · 64GB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.DisplayLabel(); got != tt.want {
				t.Errorf("DisplayLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestByName(t *testing.T) {
	ps := []Plan{
		{Name: "KVM1"},
		{Name: "KVM2"},
	}
	if p, ok := ByName(ps, "KVM2"); !ok || p.Name != "KVM2" {
		t.Errorf("ByName(KVM2) = %+v, %v; want KVM2, true", p, ok)
	}
	if _, ok := ByName(ps, "KVM9"); ok {
		t.Errorf("ByName(KVM9) found a plan that doesn't exist")
	}
	if _, ok := ByName(nil, "KVM1"); ok {
		t.Errorf("ByName on a nil slice should not find anything")
	}
	// Names are case-sensitive by design (no case-insensitive matching).
	if _, ok := ByName(ps, "kvm2"); ok {
		t.Errorf("ByName should be case-sensitive, but matched %q", "kvm2")
	}
}

func TestPath(t *testing.T) {
	t.Run("honors HLAB_HOME", func(t *testing.T) {
		t.Setenv("HLAB_HOME", "/opt/hlab")
		got, err := Path()
		if err != nil {
			t.Fatalf("Path() error: %v", err)
		}
		want := filepath.Join("/opt/hlab", "plans.yaml")
		if got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.hlab", func(t *testing.T) {
		t.Setenv("HLAB_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got, err := Path()
		if err != nil {
			t.Fatalf("Path() error: %v", err)
		}
		want := filepath.Join(home, ".hlab", "plans.yaml")
		if got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

func TestLoadSeedsFromEmbeddedDefault(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("plans.yaml should not exist yet, stat err = %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("Load() returned no plans after seeding")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("Load() should have seeded %s: %v", p, err)
	}

	// A second Load should read the now-existing file, not re-seed it, and
	// return the same data.
	got2, err := Load()
	if err != nil {
		t.Fatalf("second Load() error: %v", err)
	}
	if len(got2) != len(got) {
		t.Errorf("second Load() returned %d plans, want %d", len(got2), len(got))
	}
}

func TestLoadLXCFallsBackToEmbeddedDefaultWhenSectionMissing(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Simulate an on-disk plans.yaml that predates LXC support: only `plans:`,
	// no `lxc:` section at all.
	oldFile := "plans:\n  - name: KVM2\n    cores: 2\n    memory_gb: 4\n    disk_gb: 32\n"
	if err := os.WriteFile(p, []byte(oldFile), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lxc, err := LoadLXC()
	if err != nil {
		t.Fatalf("LoadLXC() error: %v", err)
	}
	if len(lxc) == 0 {
		t.Fatalf("LoadLXC() should fall back to the embedded default LXC tiers, got none")
	}

	// The VM plans on disk should be untouched (still just KVM2).
	vmPlans, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(vmPlans) != 1 || vmPlans[0].Name != "KVM2" {
		t.Errorf("Load() = %+v, want the single on-disk KVM2 plan preserved", vmPlans)
	}
}

func TestLoadLXCMigratesAppendsSectionToDisk(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A pre-M6 plans.yaml: only `plans:`, with an operator comment/edit we must
	// preserve through the migration (a full re-marshal would drop it).
	oldFile := "# my custom sizes\nplans:\n  - name: KVM2 # bumped\n    cores: 2\n    memory_gb: 4\n    disk_gb: 32\n"
	if err := os.WriteFile(p, []byte(oldFile), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lxc, err := LoadLXC()
	if err != nil {
		t.Fatalf("LoadLXC() error: %v", err)
	}
	if len(lxc) == 0 {
		t.Fatalf("LoadLXC() returned no LXC tiers after migration")
	}

	// The `lxc:` block must now be written back to disk (so it's editable), and
	// the operator's `plans:` comments must survive verbatim.
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(after)
	if !strings.Contains(s, "\nlxc:") {
		t.Errorf("migrated file has no lxc: section:\n%s", s)
	}
	if !strings.Contains(s, "# my custom sizes") || !strings.Contains(s, "KVM2 # bumped") {
		t.Errorf("migration dropped the operator's plans: comments:\n%s", s)
	}

	// Migration is idempotent: a second load must not append the block again.
	if _, err := LoadLXC(); err != nil {
		t.Fatalf("second LoadLXC() error: %v", err)
	}
	after2, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile (second): %v", err)
	}
	if string(after2) != s {
		t.Errorf("migration is not idempotent; file changed on second load:\n%s", string(after2))
	}
}

func TestLoadLXCUsesOnDiskSectionWhenPresent(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	custom := "plans:\n  - name: KVM2\nlxc:\n  - name: custom-tier\n    cores: 3\n    memory_mb: 777\n    disk_gb: 9\n"
	if err := os.WriteFile(p, []byte(custom), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lxc, err := LoadLXC()
	if err != nil {
		t.Fatalf("LoadLXC() error: %v", err)
	}
	if len(lxc) != 1 || lxc[0].Name != "custom-tier" || lxc[0].MemoryMB != 777 {
		t.Errorf("LoadLXC() = %+v, want the single on-disk custom-tier plan", lxc)
	}
}

func TestLoadDocParseErrorIsWrapped(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load() with malformed YAML should return an error")
	}
}
