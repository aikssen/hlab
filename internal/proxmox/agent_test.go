package proxmox

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
