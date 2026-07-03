package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

const samplePubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST user@host"

func TestLooksLikeSSHPublicKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ssh-ed25519 AAAA...", true},
		{"ssh-rsa AAAA...", true},
		{"ecdsa-sha2-nistp256 AAAA...", true},
		{"sk-ssh-ed25519@openssh.com AAAA...", true},
		{"  ssh-ed25519 AAAA...", true},
		{"/home/user/.ssh/id_ed25519.pub", false},
		{"~/.ssh/id_ed25519.pub", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeSSHPublicKey(c.in); got != c.want {
			t.Errorf("looksLikeSSHPublicKey(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveSSHPublicKeyLiteral(t *testing.T) {
	got, err := resolveSSHPublicKey("  " + samplePubKey + "  ")
	if err != nil {
		t.Fatalf("literal key: %v", err)
	}
	if got != samplePubKey {
		t.Errorf("literal key = %q, want %q (trimmed)", got, samplePubKey)
	}
}

func TestResolveSSHPublicKeyFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(path, []byte(samplePubKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	got, err := resolveSSHPublicKey(path)
	if err != nil {
		t.Fatalf("path key: %v", err)
	}
	if got != samplePubKey {
		t.Errorf("path key = %q, want %q", got, samplePubKey)
	}
}

func TestResolveSSHPublicKeyMissingPath(t *testing.T) {
	if _, err := resolveSSHPublicKey(filepath.Join(t.TempDir(), "nope.pub")); err == nil {
		t.Fatal("expected an error for a nonexistent key path")
	}
}

func TestResolveSSHPublicKeyBadContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-key.pub")
	if err := os.WriteFile(path, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := resolveSSHPublicKey(path); err == nil {
		t.Fatal("expected an error when the file has no public key")
	}
}
