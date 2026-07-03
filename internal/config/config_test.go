package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDefaultUsername(t *testing.T) {
	got := DefaultUsername()
	if got == "" {
		t.Fatal("DefaultUsername() = empty, want a non-empty username")
	}
	if strings.ContainsRune(got, '\\') {
		t.Errorf("DefaultUsername() = %q, want no DOMAIN\\ prefix", got)
	}
}

func TestMigrateLegacy(t *testing.T) {
	home := t.TempDir() // stands in for ~ (os.UserHomeDir / collapseHome)
	t.Setenv("HOME", home)
	hlabHome := t.TempDir()
	t.Setenv("HLAB_HOME", hlabHome)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	legacy := filepath.Join(xdg, "hlab")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("token_secret: sekret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "plans.yaml"), []byte("plans: []\n"), 0o644); err != nil {
		t.Fatalf("WriteFile plans: %v", err)
	}

	if err := MigrateLegacy(); err != nil {
		t.Fatalf("MigrateLegacy() error: %v", err)
	}

	// Both files land under Home() and are removed from the legacy dir (move, not copy).
	for _, name := range []string{"config.yaml", "plans.yaml"} {
		if _, err := os.Stat(filepath.Join(hlabHome, name)); err != nil {
			t.Errorf("%s should exist at Home() after migration: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(legacy, name)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed from the legacy dir, stat err = %v", name, err)
		}
	}
	// The token file is gitignored at Home() (written before the move).
	gi, err := os.ReadFile(filepath.Join(hlabHome, ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "config.yaml") {
		t.Errorf(".gitignore should list config.yaml, got:\n%s", gi)
	}
	// The now-empty legacy dir is cleaned up.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("empty legacy dir should be removed, stat err = %v", err)
	}

	// Idempotent: a second run is a no-op.
	if err := MigrateLegacy(); err != nil {
		t.Fatalf("second MigrateLegacy() error: %v", err)
	}
}

func TestMigrateLegacyDoesNotOverwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hlabHome := t.TempDir()
	t.Setenv("HLAB_HOME", hlabHome)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	legacy := filepath.Join(xdg, "hlab")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("legacy: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile legacy: %v", err)
	}
	// A config already at Home() must win — migration never clobbers it.
	if err := os.WriteFile(filepath.Join(hlabHome, "config.yaml"), []byte("home: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile home: %v", err)
	}

	if err := MigrateLegacy(); err != nil {
		t.Fatalf("MigrateLegacy() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(hlabHome, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "home: true\n" {
		t.Errorf("migration overwrote an existing config at Home(): %q", data)
	}
	// The legacy file is left in place (not moved over an existing destination).
	if _, err := os.Stat(filepath.Join(legacy, "config.yaml")); err != nil {
		t.Errorf("legacy config should be left untouched, stat err = %v", err)
	}
}

// TestMigrateLegacyDefaultDir exercises legacyDir's non-XDG branch: with no
// $XDG_CONFIG_HOME set, the pre-M8 location is ~/.config/hlab.
func TestMigrateLegacyDefaultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	hlabHome := t.TempDir()
	t.Setenv("HLAB_HOME", hlabHome)

	legacy := filepath.Join(home, ".config", "hlab")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("token_secret: sekret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := MigrateLegacy(); err != nil {
		t.Fatalf("MigrateLegacy() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hlabHome, "config.yaml")); err != nil {
		t.Errorf("config.yaml should move out of ~/.config/hlab to Home(): %v", err)
	}
}

func TestCollapseHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A path under $HOME collapses to a ~-prefixed form.
	under := filepath.Join(home, ".hlab", "config.yaml")
	if got := collapseHome(under); got != "~/.hlab/config.yaml" {
		t.Errorf("collapseHome(%q) = %q, want ~/.hlab/config.yaml", under, got)
	}
	// A path outside $HOME is returned unchanged.
	if got := collapseHome("/etc/hlab/config.yaml"); got != "/etc/hlab/config.yaml" {
		t.Errorf("collapseHome(outside home) = %q, want it unchanged", got)
	}
}

func TestPath(t *testing.T) {
	t.Run("honors HLAB_HOME", func(t *testing.T) {
		t.Setenv("HLAB_HOME", "/opt/hlab")
		got, err := Path()
		if err != nil {
			t.Fatalf("Path() error: %v", err)
		}
		want := filepath.Join("/opt/hlab", "config.yaml")
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
		want := filepath.Join(home, ".hlab", "config.yaml")
		if got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

func TestExists(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	if Exists() {
		t.Fatal("Exists() should be false before any config is written")
	}

	c := &Config{ProxmoxURL: "https://pve.example:8006/"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if !Exists() {
		t.Fatal("Exists() should be true after Save()")
	}
}

func TestLoadMissingReturnsFriendlyError(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with no config file should error")
	}
	want := "hlab is not configured yet — run `hlab setup` first"
	if err.Error() != want {
		t.Errorf("Load() error = %q, want %q", err.Error(), want)
	}
}

func TestLoadMalformedYAMLIsWrapped(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte("not: [valid: yaml"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() with malformed YAML should error")
	}
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	original := &Config{
		ProxmoxURL:  "https://pve1:8006/",
		TokenID:     "hlab@pve!token",
		TokenSecret: "super-secret",
		Insecure:    true,
		DefaultNode: "pve1",
	}
	if err := original.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file permission = %v, want 0600 (it contains a secret)", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.ProxmoxURL != original.ProxmoxURL || loaded.TokenID != original.TokenID ||
		loaded.TokenSecret != original.TokenSecret || loaded.Insecure != original.Insecure {
		t.Errorf("Load() = %+v, want a round trip of %+v", loaded, original)
	}
}

func TestApplyDefaultsViaLoad(t *testing.T) {
	hlabHome := t.TempDir()
	t.Setenv("HLAB_HOME", hlabHome)

	c := &Config{DefaultNode: "pve1", DefaultGateway: "192.168.1.1"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.DefaultStorage != "local-lvm" {
		t.Errorf("DefaultStorage default = %q, want local-lvm", loaded.DefaultStorage)
	}
	if loaded.DefaultBridge != "vmbr0" {
		t.Errorf("DefaultBridge default = %q, want vmbr0", loaded.DefaultBridge)
	}
	// Everything hlab manages lives under Home() (here, HLAB_HOME).
	if loaded.StateDirExpanded() != hlabHome {
		t.Errorf("StateDirExpanded() = %q, want %q", loaded.StateDirExpanded(), hlabHome)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0] != "pve1" {
		t.Errorf("Nodes should include the default node, got %v", loaded.Nodes)
	}
	// No dotfiles repo ships by default — empty means the dotfiles catalog entry
	// is hidden until the user configures one.
	if loaded.DotfilesRepo != "" {
		t.Errorf("DotfilesRepo default = %q, want empty", loaded.DotfilesRepo)
	}
	if loaded.DefaultCIDR != 24 {
		t.Errorf("DefaultCIDR should default to 24 when a gateway is set, got %d", loaded.DefaultCIDR)
	}
}

func TestApplyDefaultsDoesNotDuplicateExistingNode(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	c := &Config{DefaultNode: "pve1", Nodes: []string{"pve1", "pve2"}}
	if err := c.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(loaded.Nodes) != 2 {
		t.Errorf("Nodes should not duplicate an already-present default node, got %v", loaded.Nodes)
	}
}

func TestApplyDefaultsDoesNotSetCIDRWithoutGateway(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	c := &Config{}
	if err := c.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.DefaultCIDR != 0 {
		t.Errorf("DefaultCIDR should stay 0 without a gateway, got %d", loaded.DefaultCIDR)
	}
}

func TestSuggestIPCIDR(t *testing.T) {
	c := &Config{DefaultGateway: "192.168.1.1"}

	t.Run("no gateway returns empty", func(t *testing.T) {
		empty := &Config{}
		if got := empty.SuggestIPCIDR(nil); got != "" {
			t.Errorf("SuggestIPCIDR() with no gateway = %q, want empty", got)
		}
	})

	t.Run("malformed gateway returns empty", func(t *testing.T) {
		bad := &Config{DefaultGateway: "not-an-ip"}
		if got := bad.SuggestIPCIDR(nil); got != "" {
			t.Errorf("SuggestIPCIDR() with a malformed gateway = %q, want empty", got)
		}
	})

	t.Run("suggests .10 by default", func(t *testing.T) {
		got := c.SuggestIPCIDR(nil)
		want := "192.168.1.10/24"
		if got != want {
			t.Errorf("SuggestIPCIDR() = %q, want %q", got, want)
		}
	})

	t.Run("skips used addresses", func(t *testing.T) {
		used := map[string]bool{
			"192.168.1.10": true,
			"192.168.1.11": true,
		}
		got := c.SuggestIPCIDR(used)
		want := "192.168.1.12/24"
		if got != want {
			t.Errorf("SuggestIPCIDR() = %q, want %q", got, want)
		}
	})

	t.Run("uses the configured CIDR prefix", func(t *testing.T) {
		c2 := &Config{DefaultGateway: "10.0.0.1", DefaultCIDR: 16}
		got := c2.SuggestIPCIDR(nil)
		want := "10.0.0.10/16"
		if got != want {
			t.Errorf("SuggestIPCIDR() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to .10 when the whole range is used", func(t *testing.T) {
		used := map[string]bool{}
		for host := 10; host < 250; host++ {
			used["192.168.1."+strconv.Itoa(host)] = true
		}
		got := c.SuggestIPCIDR(used)
		want := "192.168.1.10/24"
		if got != want {
			t.Errorf("SuggestIPCIDR() with a full range = %q, want %q", got, want)
		}
	})
}

func TestStateDirExpanded(t *testing.T) {
	t.Run("honors HLAB_HOME", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HLAB_HOME", home)
		c := &Config{}
		if got := c.StateDirExpanded(); got != home {
			t.Errorf("StateDirExpanded() = %q, want %q", got, home)
		}
	})

	t.Run("defaults to ~/.hlab", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HLAB_HOME", "")
		t.Setenv("HOME", home)
		c := &Config{}
		want := filepath.Join(home, ".hlab")
		if got := c.StateDirExpanded(); got != want {
			t.Errorf("StateDirExpanded() = %q, want %q", got, want)
		}
	})
}

func TestSSHKeyByName(t *testing.T) {
	c := &Config{SSHKeys: []SSHKey{
		{Name: "laptop", Pub: "ssh-ed25519 AAAA laptop"},
		{Name: "desktop", Pub: "ssh-ed25519 BBBB desktop"},
	}}

	if pub, ok := c.SSHKeyByName("laptop"); !ok || pub != "ssh-ed25519 AAAA laptop" {
		t.Errorf("SSHKeyByName(laptop) = %q, %v; want the laptop key, true", pub, ok)
	}
	if _, ok := c.SSHKeyByName("phone"); ok {
		t.Error("SSHKeyByName(phone) should not be found")
	}
}

func TestScanSSHKeys(t *testing.T) {
	t.Run("no .ssh directory returns nil, no error", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		keys, err := ScanSSHKeys()
		if err != nil {
			t.Fatalf("ScanSSHKeys() error: %v", err)
		}
		if len(keys) != 0 {
			t.Errorf("ScanSSHKeys() = %v, want empty", keys)
		}
	})

	t.Run("finds .pub files and skips others", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		sshDir := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(sshDir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("ssh-ed25519 AAAA me@host\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		// Private key (no .pub suffix) must be skipped.
		if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("PRIVATE KEY DATA"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		// A subdirectory must be skipped too.
		if err := os.MkdirAll(filepath.Join(sshDir, "subdir.pub"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		keys, err := ScanSSHKeys()
		if err != nil {
			t.Fatalf("ScanSSHKeys() error: %v", err)
		}
		if len(keys) != 1 {
			t.Fatalf("ScanSSHKeys() = %v, want exactly 1 key", keys)
		}
		if keys[0].Name != "id_ed25519" {
			t.Errorf("key name = %q, want id_ed25519", keys[0].Name)
		}
		if keys[0].Pub != "ssh-ed25519 AAAA me@host" {
			t.Errorf("key pub = %q, want trimmed contents", keys[0].Pub)
		}
	})
}

func TestDefaultUserRoundTrip(t *testing.T) {
	t.Setenv("HLAB_HOME", t.TempDir())

	if err := (&Config{ProxmoxURL: "https://pve:8006/", DefaultUser: "ever"}).Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.DefaultUser != "ever" {
		t.Errorf("DefaultUser round trip = %q, want %q", loaded.DefaultUser, "ever")
	}
}

func TestCreateUserDefaultPrecedence(t *testing.T) {
	// DefaultUser, when set, wins over the OS user (DefaultUsername).
	c := &Config{DefaultUser: "webadmin"}
	if got := c.CreateUserDefault(); got != "webadmin" {
		t.Errorf("CreateUserDefault() = %q, want %q (DefaultUser should win)", got, "webadmin")
	}

	// With no DefaultUser it falls back to DefaultUsername().
	empty := &Config{}
	if got, want := empty.CreateUserDefault(), DefaultUsername(); got != want {
		t.Errorf("CreateUserDefault() = %q, want DefaultUsername() = %q", got, want)
	}
}
