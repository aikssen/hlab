package engine

import (
	"errors"
	"testing"

	"github.com/aikssen/hlab/internal/state"
)

// TestInjectSSHKeyViaConsoleWithPasswordUsesGivenPassword verifies the supplied
// password reaches the console login (rather than a secrets-file lookup) and that
// the key is appended and persisted.
func TestInjectSSHKeyViaConsoleWithPasswordUsesGivenPassword(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{}
	e := newTestEngine(t, r, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Username: "root", DHCP: false, IPCIDR: "192.168.1.101/24"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.InjectSSHKeyViaConsoleWithPassword(ct, testPubKey, "entered-pw"); err != nil {
		t.Fatalf("InjectSSHKeyViaConsoleWithPassword: %v", err)
	}
	if pm.consoleCalls != 1 {
		t.Fatalf("console exec should run once, ran %d", pm.consoleCalls)
	}
	if pm.consoleLogin.User != "root" || pm.consoleLogin.Password != "entered-pw" {
		t.Errorf("console login = %+v, want root / entered-pw", pm.consoleLogin)
	}
	if len(pm.consoleScript) != 1 {
		t.Errorf("expected one console command, got %v", pm.consoleScript)
	}
}

// TestInjectSSHKeyViaConsoleWithPasswordEmptyGuard checks that an empty password
// (none stored, none entered) fails clearly and never touches the console.
func TestInjectSSHKeyViaConsoleWithPasswordEmptyGuard(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{}
	e := newTestEngine(t, r, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Username: "root"}

	if err := e.InjectSSHKeyViaConsoleWithPassword(ct, testPubKey, ""); err == nil {
		t.Fatal("expected an error for an empty password")
	}
	if pm.consoleCalls != 0 {
		t.Errorf("empty-password guard must not reach the console (called %d times)", pm.consoleCalls)
	}
}

// TestInjectSSHKeyViaConsoleRejectsVM keeps the LXC-only guard: a VM has no
// console automation.
func TestInjectSSHKeyViaConsoleRejectsVM(t *testing.T) {
	e := newTestEngine(t, &fakeRunner{}, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm"}
	if err := e.InjectSSHKeyViaConsoleWithPassword(vm, testPubKey, "pw"); err == nil {
		t.Fatal("expected VM console injection to be rejected")
	}
}

// TestInjectSSHKeyViaConsoleUsesStoredPassword verifies the stored-password path:
// InjectSSHKeyViaConsole (no explicit password) reads the secrets file via the
// runner and threads that password to the console login.
func TestInjectSSHKeyViaConsoleUsesStoredPassword(t *testing.T) {
	r := &fakeRunner{passwords: map[string]string{"dns": "stored-pw"}}
	pm := &fakeProxmox{}
	e := newTestEngine(t, r, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Username: "root", DHCP: false, IPCIDR: "192.168.1.101/24"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.InjectSSHKeyViaConsole(ct, testPubKey); err != nil {
		t.Fatalf("InjectSSHKeyViaConsole: %v", err)
	}
	if pm.consoleLogin.Password != "stored-pw" {
		t.Errorf("console login password = %q, want the stored password", pm.consoleLogin.Password)
	}
}

// TestStoredCTPassword covers the helper callers use to decide whether to prompt.
func TestStoredCTPassword(t *testing.T) {
	r := &fakeRunner{passwords: map[string]string{"dns": "stored-pw"}}
	e := newTestEngine(t, r, &fakeProxmox{})

	pw, err := e.StoredCTPassword("dns")
	if err != nil {
		t.Fatalf("StoredCTPassword: %v", err)
	}
	if pw != "stored-pw" {
		t.Errorf("StoredCTPassword(dns) = %q, want stored-pw", pw)
	}
	if pw, _ := e.StoredCTPassword("absent"); pw != "" {
		t.Errorf("StoredCTPassword(absent) = %q, want empty", pw)
	}
}

// TestStoredCTPasswordSurfacesError propagates a secrets-file read failure.
func TestStoredCTPasswordSurfacesError(t *testing.T) {
	e := newTestEngine(t, &errPasswordRunner{}, &fakeProxmox{})
	if _, err := e.StoredCTPassword("dns"); err == nil {
		t.Fatal("expected StoredCTPassword to surface the runner error")
	}
}

// errPasswordRunner is a fakeRunner whose ExistingPasswords fails.
type errPasswordRunner struct{ fakeRunner }

func (errPasswordRunner) ExistingPasswords() (map[string]string, error) {
	return nil, errors.New("secrets unreadable")
}
