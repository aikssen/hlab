package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/aikssen/hlab/internal/state"
)

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST laptop"

func TestAddSSHKeyVMAppliesAndPersists(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.AddSSHKey(vm, testPubKey); err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}
	if r.applyCalls != 1 {
		t.Errorf("VM add-ssh-key must apply once to fold the key into state (applied %d times)", r.applyCalls)
	}
	got, err := e.Store.Load("web")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Contains(got.SSHKeys, testPubKey) {
		t.Errorf("declaration should persist the added key, got %v", got.SSHKeys)
	}
}

func TestAddSSHKeyLXCSkipsApply(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Username: "root", DHCP: false, IPCIDR: "192.168.1.101/24"}
	if err := e.Store.Save(ct); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.AddSSHKey(ct, testPubKey); err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}
	// container.tf lifecycle-ignores initialization[0].user_account, so Terraform
	// never manages container SSH keys — nothing to apply.
	if r.applyCalls != 0 {
		t.Errorf("LXC add-ssh-key must not apply (Terraform ignores container user_account); applied %d times", r.applyCalls)
	}
	got, err := e.Store.Load("dns")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Contains(got.SSHKeys, testPubKey) {
		t.Errorf("declaration should persist the added key, got %v", got.SSHKeys)
	}
}

func TestAddSSHKeyDedup(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.AddSSHKey(vm, testPubKey); err != nil {
		t.Fatalf("first AddSSHKey: %v", err)
	}
	if err := e.AddSSHKey(vm, testPubKey); err != nil {
		t.Fatalf("second AddSSHKey: %v", err)
	}
	if r.applyCalls != 1 {
		t.Errorf("a duplicate key must be a no-op (no second apply); applied %d times", r.applyCalls)
	}
	got, err := e.Store.Load("web")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if n := slices.Contains(got.SSHKeys, testPubKey); !n || len(got.SSHKeys) != 1 {
		t.Errorf("the key must appear exactly once, got %v", got.SSHKeys)
	}
}

func TestAddSSHKeyVetoesReplace(t *testing.T) {
	r := &fakeRunner{planReplace: true, planSummary: "~ initialization"}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	err := e.AddSSHKey(vm, testPubKey)
	if err == nil {
		t.Fatal("expected AddSSHKey to refuse a plan that would force a replace")
	}
	if r.applyCalls != 0 {
		t.Errorf("a replace veto must never apply (applied %d times)", r.applyCalls)
	}
}

func TestAddSSHKeyEmptyKey(t *testing.T) {
	r := &fakeRunner{}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.AddSSHKey(vm, "   "); err == nil {
		t.Fatal("expected AddSSHKey to reject an empty key")
	}
}

func TestAddSSHKeySurfacesApplyError(t *testing.T) {
	r := &fakeRunner{applyErr: errors.New("boom")}
	e := newTestEngine(t, r, &fakeProxmox{})
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}
	if err := e.AddSSHKey(vm, testPubKey); err == nil {
		t.Fatal("expected AddSSHKey to surface an apply failure")
	}
}
