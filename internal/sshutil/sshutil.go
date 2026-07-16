// Package sshutil holds the small SSH helpers shared by the CLI and the TUI:
// installing a public key into a live guest's authorized_keys over SSH. It lives
// outside cmd so internal/tui (which cannot import cmd) can reuse the exact same
// logic behind `hlab {vm,ct} add-ssh-key` and the dashboard's inject-key action.
package sshutil

import (
	"fmt"
	"os/exec"
	"strings"
)

// AppendAuthorizedKey installs a public key into a live guest's
// ~/.ssh/authorized_keys over SSH, non-interactively (BatchMode) and
// idempotently: it creates ~/.ssh (700) and authorized_keys (600) if missing and
// appends the key only when the exact line isn't already present (grep -qxF).
//
// BatchMode=yes disables every interactive prompt (notably the password prompt),
// so this NEVER blocks asking for a password: if the guest doesn't already trust
// a key hlab can use, ssh fails with "Permission denied" instead of prompting.
// That auth failure is translated into an actionable message (see authFailureHint)
// rather than a raw ssh dump — the usual cause is a keyless guest whose sshd
// refuses password auth (e.g. an LXC container's root, PermitRootLogin
// prohibit-password), which no amount of retrying over SSH can fix.
func AppendAuthorizedKey(user, ip, pubKey string) error {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found in PATH")
	}
	cmd := exec.Command(bin,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		fmt.Sprintf("%s@%s", user, ip),
		remoteAppendCommand(pubKey),
	)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		msg := strings.TrimSpace(string(out))
		if hint := authFailureHint(msg); hint != "" {
			return fmt.Errorf("could not authenticate to %s@%s over SSH — %s", user, ip, hint)
		}
		// accept-new records an unknown host silently but refuses a changed one,
		// and that refusal is not an auth failure, so it would otherwise fall
		// through to the raw ssh dump below. It means a previous guest at this
		// address left its key behind — say what to run instead of showing the
		// wall of asterisks.
		if IsHostKeyMismatch(msg) {
			return fmt.Errorf("the host key for %s changed — a previous guest at that address left its key in known_hosts; run `hlab known-hosts clean %s` and retry", ip, ip)
		}
		return fmt.Errorf("installing the key over SSH failed: %w\n%s", cerr, msg)
	}
	return nil
}

// authFailureHint returns an actionable explanation when ssh output indicates an
// authentication failure (rather than, say, a connection or remote-shell error),
// or "" otherwise. hlab connects with BatchMode (key auth only), so a "Permission
// denied"/"publickey" failure means the guest trusts no key hlab can use — the
// only fix is to add one out of band (the Proxmox console), never a retry.
func authFailureHint(out string) string {
	if !isAuthFailure(out) {
		return ""
	}
	return "the guest trusts no SSH key hlab can use (hlab uses key auth only, never a password). " +
		"Add a key from the Proxmox console (append it to the login user's ~/.ssh/authorized_keys), then retry."
}

// isAuthFailure reports whether ssh's combined output looks like an
// authentication rejection. Kept separate (and pure) so it can be unit-tested
// without shelling out to ssh.
func isAuthFailure(out string) bool {
	return strings.Contains(out, "Permission denied") ||
		strings.Contains(out, "publickey") ||
		strings.Contains(out, "No supported authentication methods")
}

// KeylessAddKeyError is the error returned by the add-ssh-key / inject-key flows
// when a key cannot be installed at all. For a VM the recovery channel is the QEMU
// guest agent (engine.InjectSSHKeyViaAgent), so the VM branch is the fallback shown
// only when that channel can't be used — the agent isn't running or the token lacks
// VM.GuestAgent.Unrestricted (the real agent/privilege error is threaded up instead
// where possible). For a container hlab CAN inject the first key over the Proxmox console using
// the root password: it uses the stored password, or prompts for it (or takes
// `--password`), so a keyless container is recoverable as long as you know the root
// password. This error is for the case where even that is impossible (no password
// stored, none entered, and no prompt available — e.g. a non-interactive run). The
// fix is to supply the root password or seed a key out of band. Shared by the CLI
// handler, the TUI inject flow and the engine's console injection.
func KeylessAddKeyError(name string, isLXC bool) error {
	if isLXC {
		return fmt.Errorf("%q has no SSH key and no root password is available (none stored, none entered), so hlab "+
			"can neither reach it over SSH nor log in to the Proxmox console to install one. Re-run interactively to be "+
			"prompted for the root password, pass --password, add a key manually from the Proxmox console "+
			"(append it to /root/.ssh/authorized_keys), or recreate the container with an SSH key or a password", name)
	}
	return fmt.Errorf("%q has no SSH key hlab can use — hlab connects over SSH with key auth only (never a "+
		"password), so the first key must go in through the QEMU guest agent. That needs qemu-guest-agent "+
		"running in the VM and the VM.GuestAgent.Unrestricted privilege on the API token "+
		"(`pveum role modify HLab --privs \"VM.GuestAgent.Unrestricted\" --append 1`). Ensure both, then retry — "+
		"or add a key from the Proxmox console (append it to the login user's ~/.ssh/authorized_keys)", name)
}

// AuthorizedKeyCommand returns the idempotent shell command that appends pubKey
// to the login user's ~/.ssh/authorized_keys. Shared by both install paths — SSH
// (AppendAuthorizedKey) and the Proxmox console (engine.InjectSSHKeyViaConsole,
// which logs in as root, where ~ is /root) — so the key is installed identically
// however hlab reaches the guest.
func AuthorizedKeyCommand(pubKey string) string { return remoteAppendCommand(pubKey) }

// AuthorizedKeyCommandFor returns the idempotent `/bin/sh -c` script that appends
// an SSH public key to a NAMED user's ~/.ssh/authorized_keys with correct
// ownership and permissions. Unlike AuthorizedKeyCommand (which targets the
// connection user's ~, i.e. root over the LXC console), this resolves the user's
// home via `getent passwd` and chowns everything to that user — it is used by the
// VM guest-agent path (engine.InjectSSHKeyViaAgent), where the agent runs as root
// but the key belongs to the unprivileged login user.
//
// The key is NOT embedded in the script: it is read from stdin (`key=$(cat)`), so
// the caller feeds it via the guest agent's input-data. That avoids shell-quoting
// the key into the command entirely — the script is identical for every key.
func AuthorizedKeyCommandFor(user string) string {
	u := shellSingleQuote(user)
	// One `set -e` pipeline: resolve the home, read the key from stdin, ensure a
	// 0700 ~/.ssh owned by the user, append the key only when absent (grep -qxF),
	// then pin authorized_keys to 0600 owned by the user.
	return fmt.Sprintf(
		"set -e; user=%s; "+
			"home=$(getent passwd \"$user\" | cut -d: -f6); "+
			"[ -n \"$home\" ] || { echo \"no home directory for $user\" >&2; exit 1; }; "+
			"key=$(cat); "+
			"install -d -m700 -o \"$user\" -g \"$user\" \"$home/.ssh\"; "+
			"touch \"$home/.ssh/authorized_keys\"; "+
			"grep -qxF -- \"$key\" \"$home/.ssh/authorized_keys\" || printf '%%s\\n' \"$key\" >> \"$home/.ssh/authorized_keys\"; "+
			"chmod 600 \"$home/.ssh/authorized_keys\"; "+
			"chown \"$user:$user\" \"$home/.ssh/authorized_keys\"",
		u,
	)
}

// remoteAppendCommand builds the remote shell command that idempotently appends
// pubKey to ~/.ssh/authorized_keys. Factored out (and unexported) so its exact
// construction is unit-testable without shelling out to ssh.
func remoteAppendCommand(pubKey string) string {
	q := shellSingleQuote(strings.TrimSpace(pubKey))
	return fmt.Sprintf(
		"set -e; mkdir -p ~/.ssh && chmod 700 ~/.ssh && "+
			"touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && "+
			"{ grep -qxF -- %s ~/.ssh/authorized_keys || printf '%%s\\n' %s >> ~/.ssh/authorized_keys; }",
		q, q,
	)
}

// shellSingleQuote wraps s in single quotes for safe embedding in a remote shell
// command, escaping any embedded single quotes.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
