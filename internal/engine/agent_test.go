package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
)

// TestInjectSSHKeyViaAgentHappyPath: a clean guest-agent exec (exit 0) appends the
// key and then persists + reconciles it via AddSSHKey (a VM applies once).
func TestInjectSSHKeyViaAgentHappyPath(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{agentResult: proxmox.AgentExecResult{ExitCode: 0}}
	e := newTestEngine(t, r, pm)
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Username: "ever", DHCP: true}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed declaration: %v", err)
	}

	if err := e.InjectSSHKeyViaAgent(vm, testPubKey); err != nil {
		t.Fatalf("InjectSSHKeyViaAgent: %v", err)
	}
	if pm.agentCalls != 1 {
		t.Errorf("agent exec should run once, ran %d", pm.agentCalls)
	}
	// argv is /bin/sh -c <script>, and the key rides on stdin (input-data).
	if len(pm.agentArgv) != 3 || pm.agentArgv[0] != "/bin/sh" || pm.agentArgv[1] != "-c" {
		t.Errorf("argv = %v, want /bin/sh -c <script>", pm.agentArgv)
	}
	if !strings.Contains(pm.agentArgv[2], "user='ever'") {
		t.Errorf("script should target the connection user, got: %s", pm.agentArgv[2])
	}
	if string(pm.agentInput) != testPubKey {
		t.Errorf("input-data = %q, want the public key", pm.agentInput)
	}
	if r.applyCalls != 1 {
		t.Errorf("a VM must apply once to fold the key into state, applied %d", r.applyCalls)
	}
	got, err := e.Store.Load("web")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Contains(got.SSHKeys, testPubKey) {
		t.Errorf("declaration should persist the key, got %v", got.SSHKeys)
	}
}

// TestInjectSSHKeyViaAgentPingFails: an agent that isn't up stops before any exec
// and never records or applies anything.
func TestInjectSSHKeyViaAgentPingFails(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{agentPingErr: errors.New("guest agent not responding")}
	e := newTestEngine(t, r, pm)
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Username: "ever"}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := e.InjectSSHKeyViaAgent(vm, testPubKey); err == nil {
		t.Fatal("expected an error when the agent ping fails")
	}
	if pm.agentCalls != 0 {
		t.Errorf("a failed ping must not reach exec (ran %d)", pm.agentCalls)
	}
	if r.applyCalls != 0 {
		t.Errorf("nothing should be applied when the ping fails (applied %d)", r.applyCalls)
	}
}

// TestInjectSSHKeyViaAgentNonZeroExit surfaces the command's stderr and does NOT
// persist the key.
func TestInjectSSHKeyViaAgentNonZeroExit(t *testing.T) {
	r := &fakeRunner{}
	pm := &fakeProxmox{agentResult: proxmox.AgentExecResult{ExitCode: 1, ErrData: "no home directory for ever"}}
	e := newTestEngine(t, r, pm)
	vm := &state.VMSpec{Name: "web", VMID: 6100, Type: "vm", Username: "ever"}
	if err := e.Store.Save(vm); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := e.InjectSSHKeyViaAgent(vm, testPubKey)
	if err == nil || !strings.Contains(err.Error(), "no home directory for ever") {
		t.Fatalf("non-zero exit should surface ErrData, got: %v", err)
	}
	if r.applyCalls != 0 {
		t.Errorf("a failed exec must not persist/apply the key (applied %d)", r.applyCalls)
	}
	if got, _ := e.Store.Load("web"); got != nil && slices.Contains(got.SSHKeys, testPubKey) {
		t.Error("declaration must not record the key when the exec failed")
	}
}

// TestInjectSSHKeyViaAgentRejectsLXC keeps the VM-only guard: an LXC container has
// no guest agent and uses the console path instead.
func TestInjectSSHKeyViaAgentRejectsLXC(t *testing.T) {
	pm := &fakeProxmox{}
	e := newTestEngine(t, &fakeRunner{}, pm)
	ct := &state.VMSpec{Name: "dns", VMID: 6101, Type: "lxc", Username: "root"}
	if err := e.InjectSSHKeyViaAgent(ct, testPubKey); err == nil {
		t.Fatal("expected guest-agent injection to be rejected for an LXC container")
	}
	if pm.agentCalls != 0 {
		t.Errorf("LXC rejection must not reach the agent (ran %d)", pm.agentCalls)
	}
}
