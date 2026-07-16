package sshutil

import (
	"reflect"
	"testing"
)

func TestParseKnownHostsFiles(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "multiple files on one line",
			in: "checkhostip no\n" +
				"globalknownhostsfile /etc/ssh/ssh_known_hosts /etc/ssh/ssh_known_hosts2\n" +
				"userknownhostsfile /home/u/.ssh/known_hosts /home/u/.ssh/known_hosts2\n",
			want: []string{"/home/u/.ssh/known_hosts", "/home/u/.ssh/known_hosts2"},
		},
		{
			name: "global known hosts is never returned",
			in:   "globalknownhostsfile /etc/ssh/ssh_known_hosts\n",
			want: nil,
		},
		{
			name: "dev null is dropped",
			in:   "userknownhostsfile /dev/null\n",
			want: nil,
		},
		{
			name: "dev null is dropped from a list",
			in:   "userknownhostsfile /dev/null /home/u/.ssh/known_hosts\n",
			want: []string{"/home/u/.ssh/known_hosts"},
		},
		{
			name: "absent line",
			in:   "checkhostip no\nhashknownhosts yes\n",
			want: nil,
		},
		{
			name: "empty output",
			in:   "",
			want: nil,
		},
		{
			name: "keyword with no value",
			in:   "userknownhostsfile\n",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseKnownHostsFiles(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseKnownHostsFiles() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseHostKeys(t *testing.T) {
	t.Run("ssh-keyscan output", func(t *testing.T) {
		in := "# 192.168.1.50:22 SSH-2.0-OpenSSH_9.6\n" +
			"192.168.1.50 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAsomekey\n" +
			"# 192.168.1.50:22 SSH-2.0-OpenSSH_9.6\n" +
			"192.168.1.50 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY\n"
		want := map[string]string{
			"ssh-ed25519":         "AAAAC3NzaC1lZDI1NTE5AAAAsomekey",
			"ecdsa-sha2-nistp256": "AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY",
		}
		if got := parseHostKeys(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseHostKeys() = %#v, want %#v", got, want)
		}
	})

	// `ssh-keygen -F` prefixes a "# Host ... found: line N" comment, and the host
	// field is hashed when HashKnownHosts is on — neither may confuse the parser.
	t.Run("hashed ssh-keygen -F output", func(t *testing.T) {
		in := "# Host 192.168.1.50 found: line 56\n" +
			"|1|abc123hashedhost=|def456hashedsalt= ssh-ed25519 AAAAC3NzaOldKey\n"
		want := map[string]string{"ssh-ed25519": "AAAAC3NzaOldKey"}
		if got := parseHostKeys(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseHostKeys() = %#v, want %#v", got, want)
		}
	})

	t.Run("empty and malformed lines are skipped", func(t *testing.T) {
		in := "\n# only a comment\nshort line\n\n"
		if got := parseHostKeys(in); len(got) != 0 {
			t.Errorf("parseHostKeys() = %#v, want empty", got)
		}
	})
}

func TestHostKeysMismatch(t *testing.T) {
	tests := []struct {
		name     string
		recorded map[string]string
		live     map[string]string
		want     bool
	}{
		{
			name:     "same key is not a mismatch",
			recorded: map[string]string{"ssh-ed25519": "AAAAsame"},
			live:     map[string]string{"ssh-ed25519": "AAAAsame"},
			want:     false,
		},
		{
			name:     "recycled address: same algorithm, different key",
			recorded: map[string]string{"ecdsa-sha2-nistp256": "AAAAold"},
			live:     map[string]string{"ecdsa-sha2-nistp256": "AAAAnew"},
			want:     true,
		},
		{
			name:     "one algorithm of several disagrees",
			recorded: map[string]string{"ssh-ed25519": "AAAAsame", "ssh-rsa": "AAAAold"},
			live:     map[string]string{"ssh-ed25519": "AAAAsame", "ssh-rsa": "AAAAnew"},
			want:     true,
		},
		// An algorithm on only one side is normal (sshd stopped offering a type, or
		// the operator only ever accepted one) and must never cost a good entry.
		{
			name:     "algorithm recorded but not offered is not a mismatch",
			recorded: map[string]string{"ssh-rsa": "AAAAold"},
			live:     map[string]string{"ssh-ed25519": "AAAAnew"},
			want:     false,
		},
		{
			name:     "algorithm offered but not recorded is not a mismatch",
			recorded: map[string]string{"ssh-ed25519": "AAAAsame"},
			live:     map[string]string{"ssh-ed25519": "AAAAsame", "ssh-rsa": "AAAAextra"},
			want:     false,
		},
		{
			name:     "nothing recorded is not a mismatch",
			recorded: map[string]string{},
			live:     map[string]string{"ssh-ed25519": "AAAAnew"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HostKeysMismatch(tt.recorded, tt.live); got != tt.want {
				t.Errorf("HostKeysMismatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsHostKeyMismatch(t *testing.T) {
	changed := "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n" +
		"Host key verification failed."
	if !IsHostKeyMismatch(changed) {
		t.Error("IsHostKeyMismatch(changed host key) = false, want true")
	}
	if !IsHostKeyMismatch("Host key verification failed.") {
		t.Error("IsHostKeyMismatch(verification failed) = false, want true")
	}
	// An auth failure is authFailureHint's job, not this one.
	if IsHostKeyMismatch("Permission denied (publickey).") {
		t.Error("IsHostKeyMismatch(permission denied) = true, want false")
	}
	if IsHostKeyMismatch("") {
		t.Error("IsHostKeyMismatch(empty) = true, want false")
	}
}
