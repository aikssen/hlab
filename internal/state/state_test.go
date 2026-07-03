package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVMSpecKindAndIsLXC(t *testing.T) {
	tests := []struct {
		name     string
		typ      string
		wantKind string
		wantLXC  bool
	}{
		{"empty type defaults to vm (back-compat)", "", "qemu", false},
		{"explicit vm type", "vm", "qemu", false},
		{"lxc type", "lxc", "lxc", true},
		{"unknown type falls back to qemu", "bogus", "qemu", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &VMSpec{Type: tt.typ}
			if got := v.Kind(); got != tt.wantKind {
				t.Errorf("Kind() = %q, want %q", got, tt.wantKind)
			}
			if got := v.IsLXC(); got != tt.wantLXC {
				t.Errorf("IsLXC() = %v, want %v", got, tt.wantLXC)
			}
		})
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	original := &VMSpec{
		Name:     "web-01",
		Node:     "pve1",
		VMID:     6100,
		Cores:    2,
		MemoryGB: 4,
		DiskGB:   32,
		DHCP:     true,
		Username: "admin",
		Software: []string{"docker", "node"},
		SSHKeys:  []string{"ssh-ed25519 AAAA... user@host"},
	}

	if err := s.Save(original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := s.Load("web-01")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Name != original.Name || loaded.Node != original.Node ||
		loaded.VMID != original.VMID || loaded.Cores != original.Cores ||
		loaded.MemoryGB != original.MemoryGB || loaded.DiskGB != original.DiskGB ||
		loaded.Username != original.Username {
		t.Errorf("Load() = %+v, want a round trip of %+v", loaded, original)
	}
	if len(loaded.Software) != 2 || loaded.Software[0] != "docker" || loaded.Software[1] != "node" {
		t.Errorf("Software round trip mismatch: got %v", loaded.Software)
	}
}

func TestStoreLoadMigratesLegacyDotfilesBool(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	// A pre-M8 declaration with the legacy `dotfiles: true` and no dotfiles in
	// its software list.
	legacy := "name: web-01\nvmid: 6100\ndotfiles: true\nsoftware:\n  - docker\n"
	if err := os.WriteFile(filepath.Join(s.vmsDir(), "web-01.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("writing legacy declaration: %v", err)
	}

	loaded, err := s.Load("web-01")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Dotfiles {
		t.Error("Load() should zero the legacy Dotfiles bool")
	}
	if !slices.Contains(loaded.Software, "dotfiles") {
		t.Errorf("Load() should append \"dotfiles\" to Software, got %v", loaded.Software)
	}
	if !slices.Contains(loaded.Software, "docker") {
		t.Errorf("Load() dropped an existing software key, got %v", loaded.Software)
	}

	// Saving the migrated form must not re-emit `dotfiles: true` (omitempty) and
	// must keep dotfiles in the software list.
	if err := s.Save(loaded); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(s.vmsDir(), "web-01.yaml"))
	if err != nil {
		t.Fatalf("re-reading declaration: %v", err)
	}
	if strings.Contains(string(data), "dotfiles: true") {
		t.Errorf("Save() re-emitted the legacy dotfiles bool:\n%s", data)
	}
	if !strings.Contains(string(data), "- dotfiles") {
		t.Errorf("Save() did not persist the migrated software list:\n%s", data)
	}
}

func TestStoreLoadDotfilesAlreadyInSoftwareNoDuplicate(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	legacy := "name: web-01\nvmid: 6100\ndotfiles: true\nsoftware:\n  - dotfiles\n"
	if err := os.WriteFile(filepath.Join(s.vmsDir(), "web-01.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("writing legacy declaration: %v", err)
	}
	loaded, err := s.Load("web-01")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	n := 0
	for _, k := range loaded.Software {
		if k == "dotfiles" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Load() should not duplicate the dotfiles key, got %v", loaded.Software)
	}
}

func TestStoreSaveRejectsUnnamedVM(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(&VMSpec{}); err == nil {
		t.Fatal("Save() with an empty name should return an error")
	}
}

func TestStoreLoadMissingReturnsError(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Load("does-not-exist"); err == nil {
		t.Fatal("Load() of a non-existent VM should return an error")
	}
}

func TestStoreListEmptyDirReturnsNilNoError(t *testing.T) {
	// The vms/ directory doesn't exist yet (Save/Init never called).
	s := New(t.TempDir())
	vms, err := s.List()
	if err != nil {
		t.Fatalf("List() on a missing dir should not error, got: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("List() = %v, want empty", vms)
	}
}

func TestStoreListSortedByName(t *testing.T) {
	s := New(t.TempDir())
	names := []string{"zeta", "alpha", "mike"}
	for _, n := range names {
		if err := s.Save(&VMSpec{Name: n}); err != nil {
			t.Fatalf("Save(%q) error: %v", n, err)
		}
	}
	vms, err := s.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(vms) != 3 {
		t.Fatalf("List() returned %d entries, want 3", len(vms))
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, w := range want {
		if vms[i].Name != w {
			t.Errorf("List()[%d].Name = %q, want %q", i, vms[i].Name, w)
		}
	}
}

func TestStoreListIgnoresNonYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Save(&VMSpec{Name: "web"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	// A stray non-yaml file (and a subdirectory) in vms/ should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "vms", "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vms", "subdir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	vms, err := s.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "web" {
		t.Errorf("List() = %+v, want exactly [web]", vms)
	}
}

func TestStoreDelete(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(&VMSpec{Name: "temp"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if err := s.Delete("temp"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := s.Load("temp"); err == nil {
		t.Fatal("Load() after Delete() should fail")
	}
}

func TestStoreDeleteMissingReturnsError(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Delete("never-existed"); err == nil {
		t.Fatal("Delete() of a non-existent VM should return an error")
	}
}

func TestStoreInitCreatesLayout(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	for _, want := range []string{
		filepath.Join(dir, "vms"),
		filepath.Join(dir, "terraform"),
	} {
		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", want, err)
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", want)
		}
	}

	gitignore := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("expected .gitignore to exist: %v", err)
	}
	for _, want := range []string{
		"config.yaml", // the token file now lives in the same dir — must be ignored
		"terraform/secrets.auto.tfvars.json",
		"terraform/*.tfstate",
		"terraform/.terraform/",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".gitignore missing expected entry %q, got:\n%s", want, data)
		}
	}

	if _, err := exec.LookPath("git"); err == nil {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Errorf("Init() should have run `git init` when git is available: %v", err)
		}
	}
}

func TestStoreInitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	gi := filepath.Join(dir, ".gitignore")
	first, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// A second Init() finds every required line already present, so the file is
	// byte-for-byte unchanged (no duplicated entries).
	if err := s.Init(); err != nil {
		t.Fatalf("second Init() error: %v", err)
	}
	second, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(second) != string(first) {
		t.Errorf("second Init() changed the .gitignore:\nbefore:\n%s\nafter:\n%s", first, second)
	}
}

func TestEnsureGitignoreAppendsMissingKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	// An operator's existing .gitignore that already ignores one required entry
	// plus a custom line of their own.
	existing := "# my notes\nnode_modules/\nterraform/*.tfstate\n"
	if err := os.WriteFile(gi, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := EnsureGitignore(dir, RequiredGitignore); err != nil {
		t.Fatalf("EnsureGitignore() error: %v", err)
	}

	data, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	// The operator's content survives verbatim...
	if !strings.HasPrefix(s, existing) {
		t.Errorf("existing content should be preserved as a prefix, got:\n%s", s)
	}
	if !strings.Contains(s, "node_modules/") {
		t.Errorf("operator's custom entry was lost:\n%s", s)
	}
	// ...missing required entries are appended...
	if !strings.Contains(s, "config.yaml") {
		t.Errorf("config.yaml should have been appended:\n%s", s)
	}
	// ...and an already-present required entry is not duplicated.
	if n := strings.Count(s, "terraform/*.tfstate\n"); n != 1 {
		t.Errorf("terraform/*.tfstate should appear exactly once, got %d:\n%s", n, s)
	}

	// Idempotent: a second call is a no-op.
	if err := EnsureGitignore(dir, RequiredGitignore); err != nil {
		t.Fatalf("second EnsureGitignore() error: %v", err)
	}
	again, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(again) != s {
		t.Errorf("EnsureGitignore is not idempotent:\n%s", again)
	}
}

func TestStoreCommitAndPushAreNoOpWithoutGitRepo(t *testing.T) {
	// Commit/Push must be best-effort no-ops when the directory isn't (yet) a
	// git repository, e.g. before Init() ran git init.
	s := New(t.TempDir())
	if err := s.Commit("test commit"); err != nil {
		t.Errorf("Commit() on a non-git dir should be a no-op, got error: %v", err)
	}
	if err := s.Push(); err != nil {
		t.Errorf("Push() on a non-git dir should be a no-op, got error: %v", err)
	}
}
