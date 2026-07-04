// Package config manages hlab's global configuration, stored at
// ~/.hlab/config.yaml (override the home dir with $HLAB_HOME). It holds the
// Proxmox connection details (including the API token secret) plus sensible
// defaults reused by every command, so the wizard never has to ask for them again.
//
// config.yaml lives in the same ~/.hlab directory as the rest of hlab's state but
// is gitignored on purpose: it contains secrets. MigrateLegacy moves a pre-M8
// config from the old split ~/.config/hlab location.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/aikssen/hlab/internal/state"
)

// SSHKey is a public key hlab can offer when creating a VM.
type SSHKey struct {
	Name string `yaml:"name"` // friendly name, e.g. "laptop"
	Path string `yaml:"path"` // absolute path to the .pub file
	Pub  string `yaml:"pub"`  // cached contents of the public key
}

// Config is the global hlab configuration.
type Config struct {
	ProxmoxURL  string `yaml:"proxmox_url"`  // e.g. https://proxmox.example:8006/
	TokenID     string `yaml:"token_id"`     // e.g. root@pam!hlab
	TokenSecret string `yaml:"token_secret"` // the secret UUID — kept local only
	Insecure    bool   `yaml:"insecure"`     // skip TLS verification (self-signed)

	DefaultNode     string   `yaml:"default_node"`
	DefaultStorage  string   `yaml:"default_storage"`
	DefaultBridge   string   `yaml:"default_bridge"`
	DefaultTemplate string   `yaml:"default_template,omitempty"` // template name preselected in the wizard
	Nodes           []string `yaml:"nodes,omitempty"`

	// Network defaults used to pre-fill static addressing during `vm create`.
	DefaultGateway string `yaml:"default_gateway,omitempty"` // e.g. 192.168.1.1
	DefaultCIDR    int    `yaml:"default_cidr,omitempty"`    // subnet prefix, e.g. 24

	SSHKeys       []SSHKey `yaml:"ssh_keys,omitempty"`
	DefaultSSHKey string   `yaml:"default_ssh_key,omitempty"`

	// DefaultUser is the last username used for a VM create; pre-filled as the
	// default for the next one.
	DefaultUser string `yaml:"default_user,omitempty"`

	DotfilesRepo string `yaml:"dotfiles_repo,omitempty"` // optional dotfiles repo SSH URL; empty hides the dotfiles catalog entry

	Theme string `yaml:"theme,omitempty"` // color theme (one of theme.Names()). Empty/unknown falls back to github-dark, no error.
}

// Home returns hlab's home directory: $HLAB_HOME when set, else ~/.hlab. Every
// piece of hlab state — config.yaml, plans.yaml, vms/, terraform/, ansible/ —
// lives under it, in a single (optionally git-versioned) directory.
func Home() (string, error) {
	if h := os.Getenv("HLAB_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hlab"), nil
}

// Path returns the location of the config file (Home()/config.yaml).
func Path() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "config.yaml"), nil
}

// Exists reports whether a config file is present.
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Load reads the config file. It returns a friendly error if hlab has not been
// set up yet.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hlab is not configured yet — run `hlab setup` first")
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.DefaultStorage == "" {
		c.DefaultStorage = "local-lvm"
	}
	if c.DefaultBridge == "" {
		c.DefaultBridge = "vmbr0"
	}
	if c.DefaultNode != "" && !slices.Contains(c.Nodes, c.DefaultNode) {
		c.Nodes = append(c.Nodes, c.DefaultNode)
	}
	if c.DefaultCIDR == 0 && c.DefaultGateway != "" {
		c.DefaultCIDR = 24
	}
}

// SuggestIPCIDR proposes a static IPv4 (with prefix) in the gateway's /24,
// starting at .10 and skipping any address already used by a managed VM.
// Returns "" when no default gateway is configured.
func (c *Config) SuggestIPCIDR(used map[string]bool) string {
	if c.DefaultGateway == "" {
		return ""
	}
	parts := strings.Split(c.DefaultGateway, ".")
	if len(parts) != 4 {
		return ""
	}
	base := parts[0] + "." + parts[1] + "." + parts[2] + "."
	cidr := c.DefaultCIDR
	if cidr == 0 {
		cidr = 24
	}
	for host := 10; host < 250; host++ {
		ip := fmt.Sprintf("%s%d", base, host)
		if !used[ip] {
			return fmt.Sprintf("%s/%d", ip, cidr)
		}
	}
	return fmt.Sprintf("%s10/%d", base, cidr)
}

// Save writes the config file with 0600 permissions (it contains a secret).
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// StateDirExpanded returns the homelab-state directory: everything hlab manages
// lives under Home() (config.yaml, plans.yaml, vms/, terraform/, ansible/).
func (c *Config) StateDirExpanded() string {
	h, _ := Home()
	return h
}

// MigrateLegacy moves a pre-M8 config.yaml/plans.yaml from the old split location
// (~/.config/hlab, or $XDG_CONFIG_HOME/hlab) into the consolidated Home(). It is a
// silent, idempotent best-effort called on every command: each file moves only
// when absent at Home() and present in the legacy dir, printing one stderr notice
// per file. config.yaml carries the Proxmox token, so its .gitignore entry is
// ensured at Home() BEFORE the move — the destination may already be a git repo.
// Cross-device moves fall back to copy+chmod+remove. The legacy dir is removed
// afterwards when empty; cleanup never fails the migration.
func MigrateLegacy() error {
	home, err := Home()
	if err != nil {
		return err
	}
	legacy, err := legacyDir()
	if err != nil {
		return err
	}
	if legacy == "" || legacy == home {
		return nil
	}
	files := []struct {
		name string
		mode os.FileMode
	}{
		{"config.yaml", 0o600},
		{"plans.yaml", 0o644},
	}
	for _, f := range files {
		src, dst := filepath.Join(legacy, f.name), filepath.Join(home, f.name)
		if _, err := os.Stat(dst); err == nil {
			continue // already at Home() — nothing to do
		}
		if _, err := os.Stat(src); err != nil {
			continue // not present in the legacy dir
		}
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		if f.name == "config.yaml" {
			// Protect the token before it lands in what may be a git repo.
			if err := state.EnsureGitignore(home, []string{"config.yaml"}); err != nil {
				return err
			}
		}
		if err := moveFile(src, dst, f.mode); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "» migrated %s → %s\n", collapseHome(src), collapseHome(dst))
	}
	removeIfEmpty(legacy)
	return nil
}

// legacyDir returns the pre-M8 config directory (~/.config/hlab, honoring
// $XDG_CONFIG_HOME) — the location Path() used before consolidation.
func legacyDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "hlab"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "hlab"), nil
}

// moveFile renames src to dst, falling back to copy+chmod+remove across devices
// (os.Rename returns EXDEV when src and dst are on different filesystems).
func moveFile(src, dst string, mode os.FileMode) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}
	return os.Remove(src)
}

// removeIfEmpty removes dir when it has no remaining entries. Best-effort.
func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return
	}
	_ = os.Remove(dir)
}

// collapseHome rewrites an absolute path under the user's home to a ~-prefixed
// form for a friendlier notice.
func collapseHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// SSHKeyByName returns the public key contents for a configured key name.
func (c *Config) SSHKeyByName(name string) (string, bool) {
	for _, k := range c.SSHKeys {
		if k.Name == name {
			return k.Pub, true
		}
	}
	return "", false
}

// DefaultUsername returns a sensible default administrative username for new
// guests: the current OS user (with any "DOMAIN\" prefix stripped), falling back
// to "admin" when it can't be determined.
func DefaultUsername() string {
	u, err := user.Current()
	if err != nil {
		return "admin"
	}
	name := u.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "admin"
	}
	return name
}

// CreateUserDefault returns the username to pre-fill when creating a new VM: the
// last-used username (DefaultUser) when set, else DefaultUsername() (the OS user,
// falling back to "admin"). Successful VM creates persist the chosen name back to
// DefaultUser, so the next create defaults to whatever was used last.
func (c *Config) CreateUserDefault() string {
	if c.DefaultUser != "" {
		return c.DefaultUser
	}
	return DefaultUsername()
}

// ScanSSHKeys looks in ~/.ssh for *.pub files and returns them as candidate
// SSHKey entries (name derived from the filename). Used by `hlab setup` so the
// user can pick existing keys instead of pasting them.
func ScanSSHKeys() ([]SSHKey, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []SSHKey
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".pub")
		keys = append(keys, SSHKey{
			Name: name,
			Path: path,
			Pub:  strings.TrimSpace(string(data)),
		})
	}
	return keys, nil
}
