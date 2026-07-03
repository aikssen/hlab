package tui

import (
	"testing"

	"github.com/aikssen/hlab/internal/config"
)

// TestOperatorSSHKeysDedup verifies the configured keys come first, in order, and
// duplicates by contents are dropped. Keys scanned from ~/.ssh are appended after
// (not asserted here, since that read is environment-dependent), so the configured
// prefix is what this pins.
func TestOperatorSSHKeysDedup(t *testing.T) {
	cfg := &config.Config{SSHKeys: []config.SSHKey{
		{Name: "laptop", Path: "/a/laptop.pub", Pub: "ssh-ed25519 AAAA laptop"},
		{Name: "dup", Path: "/a/dup.pub", Pub: "ssh-ed25519 AAAA laptop"}, // same Pub → dropped
		{Name: "empty", Path: "/a/empty.pub", Pub: "   "},                 // blank → dropped
		{Name: "desktop", Path: "/a/desktop.pub", Pub: "ssh-ed25519 BBBB desktop"},
	}}
	keys := operatorSSHKeys(cfg)
	if len(keys) < 2 {
		t.Fatalf("expected at least the 2 unique config keys, got %d", len(keys))
	}
	if keys[0].Name != "laptop" || keys[1].Name != "desktop" {
		t.Errorf("config prefix = [%s %s], want [laptop desktop]", keys[0].Name, keys[1].Name)
	}
}

// TestNewInjectBindingPreselect checks the configured default key is preselected.
func TestNewInjectBindingPreselect(t *testing.T) {
	cfg := &config.Config{
		DefaultSSHKey: "desktop",
		SSHKeys: []config.SSHKey{
			{Name: "laptop", Path: "/a/laptop.pub", Pub: "ssh-ed25519 AAAA laptop"},
			{Name: "desktop", Path: "/a/desktop.pub", Pub: "ssh-ed25519 BBBB desktop"},
		},
	}
	b, err := newInjectBinding(cfg, "web", false)
	if err != nil {
		t.Fatalf("newInjectBinding: %v", err)
	}
	if b.pub != "ssh-ed25519 BBBB desktop" {
		t.Errorf("preselected key = %q, want the desktop key", b.pub)
	}
}
