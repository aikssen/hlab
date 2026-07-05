package proxmox

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseMeminfo uses the real /proc/meminfo of test VM 6500: the balloon figure
// (Total-Free) reads ~89% used, but the honest figure (Total-Available) is ~39% —
// the difference being ~3.5 GB of reclaimable page cache.
func TestParseMeminfo(t *testing.T) {
	const meminfo = `MemTotal:        6062116 kB
MemFree:          451112 kB
MemAvailable:    3677420 kB
Buffers:          217564 kB
Cached:          2998660 kB
SReclaimable:     120000 kB
SwapTotal:             0 kB
SwapFree:              0 kB`
	gm, err := parseMeminfo(meminfo)
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	if gm.TotalMB != 6062116/1024 { // 5920
		t.Errorf("TotalMB = %d, want %d", gm.TotalMB, 6062116/1024)
	}
	// Used is Total-Available (2384696 kB), NOT Total-Free — cache stays out of used.
	if want := (6062116 - 3677420) / 1024; gm.UsedMB != want { // 2328
		t.Errorf("UsedMB = %d, want %d (Total-Available, not Total-Free)", gm.UsedMB, want)
	}
	if want := 3677420 / 1024; gm.AvailMB != want {
		t.Errorf("AvailMB = %d, want %d", gm.AvailMB, want)
	}
	if want := (217564 + 2998660 + 120000) / 1024; gm.CacheMB != want {
		t.Errorf("CacheMB = %d, want %d", gm.CacheMB, want)
	}
	// The honest usage is ~39%, a world away from the balloon's ~89%.
	if frac := float64(gm.UsedMB) / float64(gm.TotalMB); frac < 0.37 || frac > 0.41 {
		t.Errorf("used fraction = %.2f, want ~0.39", frac)
	}
}

// TestParseMeminfoNoMemAvailable exercises the pre-3.14 fallback (MemFree+Buffers+
// Cached) and the missing-MemTotal error.
func TestParseMeminfoNoMemAvailable(t *testing.T) {
	gm, err := parseMeminfo("MemTotal: 1048576 kB\nMemFree: 262144 kB\nBuffers: 131072 kB\nCached: 131072 kB")
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	// avail = 262144+131072+131072 = 524288 kB → used = 524288 kB = 512 MB.
	if gm.UsedMB != 512 {
		t.Errorf("UsedMB = %d, want 512 (fallback avail = free+buffers+cached)", gm.UsedMB)
	}
	if _, err := parseMeminfo("Bogus: 1 kB"); err == nil {
		t.Error("parseMeminfo without MemTotal should error")
	}
}

// TestAgentCommandValues pins the PVE 8+ array encoding: each argv element is a
// separate repeated `command` field, and input-data is a single plain field.
func TestAgentCommandValues(t *testing.T) {
	v := agentCommandValues([]string{"/bin/sh", "-c", "cat"}, []byte("the-key"))
	if got := v["command"]; len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" || got[2] != "cat" {
		t.Errorf("command fields = %v, want the argv as three repeated fields", got)
	}
	if got := v.Get("input-data"); got != "the-key" {
		t.Errorf("input-data = %q, want the-key", got)
	}
	// Encoded form carries command three times.
	if enc := v.Encode(); strings.Count(enc, "command=") != 3 {
		t.Errorf("encoded form should repeat command= three times: %s", enc)
	}
	// No input-data field when none is supplied.
	if _, ok := agentCommandValues([]string{"x"}, nil)["input-data"]; ok {
		t.Error("input-data must be omitted when no data is supplied")
	}
}

// TestDecodeAgentData covers both API shapes: base64 (the guest agent's native
// encoding) is decoded; plain text that isn't valid base64 is returned as-is.
func TestDecodeAgentData(t *testing.T) {
	if got := decodeAgentData(base64.StdEncoding.EncodeToString([]byte("hello\n"))); got != "hello\n" {
		t.Errorf("base64 out-data should decode, got %q", got)
	}
	if got := decodeAgentData("no supported key"); got != "no supported key" {
		t.Errorf("plain (non-base64) out-data should pass through, got %q", got)
	}
	if got := decodeAgentData(""); got != "" {
		t.Errorf("empty stays empty, got %q", got)
	}
}

// TestParseAgentExecStatus checks exit-code/output decoding and that `exited` is
// accepted as both the JSON number 1 and the boolean true, and that a running
// process (exited 0) reports not-yet-exited.
func TestParseAgentExecStatus(t *testing.T) {
	out := base64.StdEncoding.EncodeToString([]byte("ok\n"))
	errd := base64.StdEncoding.EncodeToString([]byte("boom\n"))
	body := fmt.Sprintf(`{"data":{"exited":1,"exitcode":3,"out-data":%q,"err-data":%q}}`, out, errd)
	res, exited, err := parseAgentExecStatus([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !exited {
		t.Error("exited:1 should be reported as exited")
	}
	if res.ExitCode != 3 || res.OutData != "ok\n" || res.ErrData != "boom\n" {
		t.Errorf("result = %+v, want exit 3 / ok / boom", res)
	}

	// exited as a JSON boolean.
	if _, exited, _ := parseAgentExecStatus([]byte(`{"data":{"exited":true,"exitcode":0}}`)); !exited {
		t.Error("exited:true should be reported as exited")
	}
	// still running.
	if _, exited, _ := parseAgentExecStatus([]byte(`{"data":{"exited":0}}`)); exited {
		t.Error("exited:0 should be reported as not-yet-exited")
	}
}

// TestAgentExecPollsUntilExit drives the full two-step flow against a fake API:
// POST /agent/exec returns a pid, and GET /agent/exec-status reports "running"
// once before "exited", so the client's poll loop is exercised end to end.
func TestAgentExecPollsUntilExit(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/exec"):
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if got := r.Form["command"]; len(got) != 3 {
				t.Errorf("exec should receive 3 command fields, got %v", got)
			}
			if got := r.Form.Get("input-data"); got != "my-key" {
				t.Errorf("input-data = %q, want my-key", got)
			}
			fmt.Fprint(w, `{"data":{"pid":42}}`)
		case strings.HasSuffix(r.URL.Path, "/agent/exec-status"):
			if r.URL.Query().Get("pid") != "42" {
				t.Errorf("exec-status pid = %q, want 42", r.URL.Query().Get("pid"))
			}
			polls++
			if polls < 2 {
				fmt.Fprint(w, `{"data":{"exited":0}}`)
				return
			}
			out := base64.StdEncoding.EncodeToString([]byte("done\n"))
			fmt.Fprintf(w, `{"data":{"exited":1,"exitcode":0,"out-data":%q}}`, out)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "root@pam!hlab", "secret", false)
	res, err := c.AgentExec("pve2", 6100, []string{"/bin/sh", "-c", "cat"}, []byte("my-key"))
	if err != nil {
		t.Fatalf("AgentExec: %v", err)
	}
	if res.ExitCode != 0 || res.OutData != "done\n" {
		t.Errorf("result = %+v, want exit 0 / done", res)
	}
	if polls < 2 {
		t.Errorf("expected the client to poll exec-status until exit, polled %d", polls)
	}
}

// TestAgentExec403NamesPrivilege verifies a 403 from agent/exec is mapped to the
// actionable VM.GuestAgent.Unrestricted message.
func TestAgentExec403NamesPrivilege(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"data":null}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := New(srv.URL, "root@pam!hlab", "secret", false)
	_, err := c.AgentExec("pve2", 6100, []string{"/bin/sh"}, nil)
	if err == nil || !strings.Contains(err.Error(), "VM.GuestAgent.Unrestricted") {
		t.Fatalf("403 should name VM.GuestAgent.Unrestricted, got: %v", err)
	}
}

// TestAgentPing covers the success and 403 paths.
func TestAgentPing(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer ok.Close()
	if err := New(ok.URL, "t", "s", false).AgentPing("pve2", 6100); err != nil {
		t.Errorf("ping should succeed, got: %v", err)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer denied.Close()
	if err := New(denied.URL, "t", "s", false).AgentPing("pve2", 6100); err == nil ||
		!strings.Contains(err.Error(), "VM.GuestAgent.Unrestricted") {
		t.Errorf("403 ping should name VM.GuestAgent.Unrestricted, got: %v", err)
	}
}
