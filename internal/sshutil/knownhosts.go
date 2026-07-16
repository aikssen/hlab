package sshutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Forget removes every host key recorded for host from the known_hosts files ssh
// would actually consult for it.
//
// hlab is what puts those entries there in the first place: AppendAuthorizedKey
// connects with StrictHostKeyChecking=accept-new, and the plain ssh behind
// `hlab {vm,ct} ssh` records the key on the usual TOFU prompt. Both write to the
// operator's real known_hosts. Nothing ever took them back out, so destroying a
// guest and creating another one at the same address (the normal homelab pattern
// — recycled test IDs and a small static-IP pool) left a stale entry behind and
// the next ssh died with "REMOTE HOST IDENTIFICATION HAS CHANGED".
//
// Callers must hook this to a *mutation* (create/destroy), never to a connection.
// At create/destroy hlab knows first-hand that the guest at host is brand new or
// gone, so the recorded key is stale by construction and dropping it decides
// nothing about trust. Dropping entries at connect time instead would silently
// turn every genuine man-in-the-middle warning into an accept, which is the one
// thing known_hosts exists to prevent.
//
// Removal goes through `ssh-keygen -R` rather than editing the file directly:
// known_hosts is often hashed (HashKnownHosts), where the hostname never appears
// in plain text and no grep/sed can find it. ssh-keygen also handles every key
// type at once and retains the previous contents as <file>.old.
func Forget(host string) error {
	bin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return fmt.Errorf("ssh-keygen not found in PATH")
	}
	files := KnownHostsFiles(host)
	if len(files) == 0 {
		return nil
	}
	var failures []string
	for _, f := range files {
		// -R is already idempotent: a host with no entry in this file exits 0
		// ("not found in <file>"), so there is nothing to pre-check.
		if out, cerr := exec.Command(bin, "-R", host, "-f", f).CombinedOutput(); cerr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v: %s", f, cerr, strings.TrimSpace(string(out))))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("removing host keys for %s: %s", host, strings.Join(failures, "; "))
	}
	return nil
}

// KnownHostsFiles returns the user known_hosts files ssh would consult for host,
// resolved through `ssh -G` so that a UserKnownHostsFile override in the
// operator's ssh_config is honoured — a bare `ssh-keygen -R` would edit the
// default file, which may not be the one in use. Files that don't exist are
// dropped: ssh-keygen -R fails outright (exit 255, "Cannot stat") on a missing
// file, and a file with no entries is nothing to clean anyway.
//
// Best-effort by design: any failure to interrogate ssh yields no files, and the
// caller simply skips the cleanup.
func KnownHostsFiles(host string) []string {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return nil
	}
	out, err := exec.Command(bin, "-G", host).Output()
	if err != nil {
		return nil
	}
	var existing []string
	for _, f := range parseKnownHostsFiles(string(out)) {
		if _, serr := os.Stat(f); serr == nil {
			existing = append(existing, f)
		}
	}
	return existing
}

// parseKnownHostsFiles extracts the user known_hosts paths from `ssh -G` output.
// ssh prints one lowercase "userknownhostsfile" line listing every configured
// file, space-separated and already tilde-expanded:
//
//	userknownhostsfile /home/u/.ssh/known_hosts /home/u/.ssh/known_hosts2
//
// globalknownhostsfile is deliberately ignored: it is system-wide and root-owned,
// not hlab's to edit. /dev/null is dropped too — it is what the ansible runner
// points ssh at, and "cleaning" it is meaningless.
//
// Paths containing spaces cannot be recovered: ssh -G prints the value unquoted,
// so they are indistinguishable from a list. That is a known and accepted limit.
func parseKnownHostsFiles(sshGOutput string) []string {
	for _, line := range strings.Split(sshGOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "userknownhostsfile") {
			continue
		}
		var files []string
		for _, f := range fields[1:] {
			if f == "/dev/null" {
				continue
			}
			files = append(files, f)
		}
		return files
	}
	return nil
}

// IsHostKeyMismatch reports whether ssh output is the "host identification has
// changed" refusal — a stale known_hosts entry — rather than any other failure.
// AppendAuthorizedKey connects with accept-new, which silently records an unknown
// host but hard-fails on a changed one, and that failure is not an auth failure,
// so authFailureHint doesn't catch it and the caller would otherwise print a raw
// ssh dump.
func IsHostKeyMismatch(sshOutput string) bool {
	s := strings.ToLower(sshOutput)
	return strings.Contains(s, "host identification has changed") ||
		strings.Contains(s, "host key verification failed")
}

// LiveHostKeys returns the host keys the guest at host currently presents, keyed
// by algorithm, read straight off the wire with ssh-keyscan. An unreachable guest
// yields an error, which callers treat as "don't touch this entry" — a host we
// cannot ask about is a host we cannot prove stale.
func LiveHostKeys(host string) (map[string]string, error) {
	bin, err := exec.LookPath("ssh-keyscan")
	if err != nil {
		return nil, fmt.Errorf("ssh-keyscan not found in PATH")
	}
	// -T bounds the wait on a guest that is off or firewalled. ssh-keyscan writes
	// progress/errors to stderr and keys to stdout, so only stdout is parsed.
	out, err := exec.Command(bin, "-T", "5", host).Output()
	if err != nil {
		return nil, fmt.Errorf("scanning host keys for %s: %w", host, err)
	}
	keys := parseHostKeys(string(out))
	if len(keys) == 0 {
		return nil, fmt.Errorf("no host keys returned by %s", host)
	}
	return keys, nil
}

// RecordedHostKeys returns the keys known_hosts currently records for host, keyed
// by algorithm. ssh-keygen -F is used rather than reading the file, so hashed
// entries resolve correctly.
func RecordedHostKeys(host string) map[string]string {
	bin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return nil
	}
	keys := map[string]string{}
	for _, f := range KnownHostsFiles(host) {
		// A host with no entry exits non-zero with no output; that is not an error
		// here, it just contributes nothing.
		out, ferr := exec.Command(bin, "-F", host, "-f", f).Output()
		if ferr != nil {
			continue
		}
		for alg, key := range parseHostKeys(string(out)) {
			keys[alg] = key
		}
	}
	return keys
}

// parseHostKeys reads "<host> <algorithm> <key>" lines, as printed by both
// ssh-keyscan and `ssh-keygen -F`, into algorithm→key. Comment lines (# …) are
// skipped: keyscan reports the server banner that way and ssh-keygen -F reports
// which line it matched. The host field is ignored precisely because it may be
// hashed in a known_hosts entry and plain in a keyscan of the same host — the
// caller already knows which host it asked about.
func parseHostKeys(output string) map[string]string {
	keys := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		keys[fields[1]] = fields[2]
	}
	return keys
}

// HostKeysMismatch reports whether anything recorded for a host contradicts what
// it actually presents — the precise definition of a stale entry, and the only
// thing `known-hosts clean --all` is allowed to remove.
//
// Only algorithms present on BOTH sides are compared. An algorithm recorded but
// not offered (or offered but not recorded) is not a contradiction: sshd may have
// stopped offering a type, or the operator may only ever have accepted one. That
// asymmetry is normal and must not cost a correct entry.
func HostKeysMismatch(recorded, live map[string]string) bool {
	for alg, rec := range recorded {
		if l, ok := live[alg]; ok && l != rec {
			return true
		}
	}
	return false
}
