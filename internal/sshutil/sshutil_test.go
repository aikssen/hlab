package sshutil

import (
	"strings"
	"testing"
)

const samplePubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST user@host"

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"plain":      "'plain'",
		"a b":        "'a b'",
		"it's":       `'it'\''s'`,
		samplePubKey: "'" + samplePubKey + "'",
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRemoteAppendCommand pins the remote command's shape: the key is embedded
// (single-quoted) for both the grep guard and the append, it is trimmed, and the
// command stays idempotent (grep -qxF before the append).
func TestRemoteAppendCommand(t *testing.T) {
	cmd := remoteAppendCommand("  " + samplePubKey + "  ")
	q := "'" + samplePubKey + "'"
	if strings.Count(cmd, q) != 2 {
		t.Errorf("expected the quoted, trimmed key twice, got:\n%s", cmd)
	}
	for _, want := range []string{
		"mkdir -p ~/.ssh && chmod 700 ~/.ssh",
		"chmod 600 ~/.ssh/authorized_keys",
		"grep -qxF -- " + q + " ~/.ssh/authorized_keys",
		"printf '%s\\n' " + q + " >> ~/.ssh/authorized_keys",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("remote command missing %q in:\n%s", want, cmd)
		}
	}
}

// TestAuthorizedKeyCommandFor pins the guest-agent (VM) script: it targets the
// named user (single-quoted), resolves the home via getent, reads the key from
// stdin (so the key is never embedded), and stays idempotent (grep -qxF before
// the append) with correct ownership/perms.
func TestAuthorizedKeyCommandFor(t *testing.T) {
	cmd := AuthorizedKeyCommandFor("ever")
	for _, want := range []string{
		"user='ever'",
		"getent passwd",
		"key=$(cat)",
		`install -d -m700 -o "$user" -g "$user" "$home/.ssh"`,
		`grep -qxF -- "$key" "$home/.ssh/authorized_keys"`,
		`chmod 600 "$home/.ssh/authorized_keys"`,
		`chown "$user:$user" "$home/.ssh/authorized_keys"`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("AuthorizedKeyCommandFor missing %q in:\n%s", want, cmd)
		}
	}
	// The key is fed on stdin, so it must never be embedded in the script.
	if strings.Contains(cmd, samplePubKey) {
		t.Errorf("the key must ride on stdin, not be embedded in:\n%s", cmd)
	}
	// A username can't break out of its single quotes.
	if !strings.Contains(AuthorizedKeyCommandFor("a'b"), `user='a'\''b'`) {
		t.Error("username single quotes must be escaped")
	}
}

// TestAuthorizedKeyCommand pins that the exported console/SSH command is exactly
// the shared remoteAppendCommand (same idempotent append into ~/.ssh), so the key
// is installed identically over SSH and over the Proxmox console.
func TestAuthorizedKeyCommand(t *testing.T) {
	if got, want := AuthorizedKeyCommand(samplePubKey), remoteAppendCommand(samplePubKey); got != want {
		t.Errorf("AuthorizedKeyCommand should equal remoteAppendCommand:\n got:  %s\n want: %s", got, want)
	}
}

func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"root@192.168.1.60: Permission denied (publickey,password).", true},
		{"Permission denied, please try again.", true},
		{"No supported authentication methods available", true},
		{"", false},
		{"ssh: connect to host 192.168.1.60 port 22: Connection refused", false},
		{"bash: grep: command not found", false},
	}
	for _, c := range cases {
		if got := isAuthFailure(c.in); got != c.want {
			t.Errorf("isAuthFailure(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAuthFailureHint(t *testing.T) {
	// A non-auth failure yields no hint (the raw error is more useful there).
	if hint := authFailureHint("Connection refused"); hint != "" {
		t.Errorf("authFailureHint(non-auth) = %q, want empty", hint)
	}
	// An auth failure yields an actionable, console-oriented hint.
	hint := authFailureHint("Permission denied (publickey).")
	if hint == "" {
		t.Fatal("authFailureHint(auth failure) = empty, want a hint")
	}
	for _, want := range []string{"key auth only", "Proxmox console", "authorized_keys"} {
		if !strings.Contains(hint, want) {
			t.Errorf("authFailureHint missing %q in:\n%s", want, hint)
		}
	}
}

func TestKeylessAddKeyError(t *testing.T) {
	lxc := KeylessAddKeyError("dns", true)
	if lxc == nil {
		t.Fatal("KeylessAddKeyError(lxc) = nil, want an error")
	}
	for _, want := range []string{`"dns"`, "root password", "console", "/root/.ssh/authorized_keys"} {
		if !strings.Contains(lxc.Error(), want) {
			t.Errorf("KeylessAddKeyError(lxc) missing %q in:\n%s", want, lxc.Error())
		}
	}
	vm := KeylessAddKeyError("web", false)
	for _, want := range []string{`"web"`, "key auth only", "console"} {
		if !strings.Contains(vm.Error(), want) {
			t.Errorf("KeylessAddKeyError(vm) missing %q in:\n%s", want, vm.Error())
		}
	}
}

// TestRemoteAppendCommandEscapesQuotes ensures a key with an embedded single
// quote is escaped via the '\” sequence rather than left able to break out of
// the surrounding single quotes.
func TestRemoteAppendCommandEscapesQuotes(t *testing.T) {
	cmd := remoteAppendCommand("ssh-ed25519 AAAA'; rm -rf / #")
	// The embedded quote must appear as the escaped '\'' sequence, and never as a
	// bare `'` that closes the quote right before the injected `; rm`.
	if !strings.Contains(cmd, `AAAA'\''; rm -rf / #`) {
		t.Errorf("single quote not escaped as '\\'' in:\n%s", cmd)
	}
}
