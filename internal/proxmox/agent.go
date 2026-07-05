package proxmox

// QEMU guest-agent command execution over the Proxmox VE REST API. hlab uses it
// for one thing: seeding an SSH key into a VM that was created with NO key. Such a
// VM is unreachable over SSH — Ubuntu cloud images ship `PasswordAuthentication
// no`, so sshd refuses the cloud-init password, and hlab connects with key auth
// only — so the first key has to be injected through a channel that needs no SSH.
// For a VM that channel is the QEMU guest agent, which runs as root inside the
// guest and can exec commands with no login: the VM analogue of the LXC console
// path in console.go (a VM's graphical console is VNC, not a text stream).
//
// The two-step API mirrors `qm guest exec` (confirmed against the Proxmox VE API
// and the PVE 8 change that turned `command` from a string into an array —
// forum.proxmox.com/threads/proxmox-8-api-agent-exec-changes.131032):
//
//  1. POST /nodes/{node}/qemu/{vmid}/agent/exec → {pid}
//     `command` is sent as REPEATED `command=` form fields (the array form, e.g.
//     command=/bin/sh&command=-c&command=…); an optional `input-data` form field
//     is fed to the command's stdin (hlab passes the public key that way to avoid
//     shell-quoting it into the script).
//  2. GET /nodes/{node}/qemu/{vmid}/agent/exec-status?pid={pid} until exited==1,
//     then read exitcode + out-data/err-data. Those data fields come from the
//     guest agent base64-encoded, but some PVE builds decode them first, so
//     decodeAgentData handles both.
//
// Reaching agent/exec needs the VM.GuestAgent.Unrestricted privilege on the API
// token; a 403 anywhere is surfaced naming that exact privilege. This is plain
// REST — no websocket, no extra dependency.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// agentExecDeadline bounds the whole exec-status poll loop: a keyless-VM key
// injection is a trivial shell command, so anything slower than this means the
// agent is wedged rather than working.
const agentExecDeadline = 30 * time.Second

// agentPollInterval is the pause between exec-status polls; the command is quick,
// so a short interval keeps latency low without hammering the API.
const agentPollInterval = 500 * time.Millisecond

// AgentExecResult is the outcome of a finished guest-agent command: the process
// exit code plus its decoded stdout/stderr.
type AgentExecResult struct {
	ExitCode int
	OutData  string
	ErrData  string
}

// agentUnrestrictedHint names the privilege a 403 from the guest-agent endpoints
// means the API token role is missing, with the exact command to grant it.
func agentUnrestrictedHint() string {
	return "the API token role is missing the VM.GuestAgent.Unrestricted privilege needed to run commands via the guest agent — grant it with `pveum role modify HLab --privs \"VM.GuestAgent.Unrestricted\" --append 1`"
}

// AgentPing reports whether the QEMU guest agent inside a VM is responding. A nil
// return means the agent answered; otherwise the error explains the likely cause
// (agent not installed/running, or the token missing the guest-agent privilege).
func (c *Client) AgentPing(node string, vmid int) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/ping", node, vmid)
	status, body, err := c.requestForm(http.MethodPost, path, url.Values{})
	if err != nil {
		return err
	}
	if status == http.StatusForbidden {
		return fmt.Errorf("Proxmox denied guest-agent access (403): %s", agentUnrestrictedHint())
	}
	if status >= 300 {
		return fmt.Errorf("guest agent not responding for VM %d on %s — is qemu-guest-agent installed and running, and the VM booted? (proxmox: %s)",
			vmid, node, strings.TrimSpace(string(body)))
	}
	return nil
}

// AgentExec runs argv inside a VM through the guest agent, feeding inputData to
// its stdin, and returns the finished command's exit code and decoded output. It
// POSTs the exec (argv as repeated `command=` fields) then polls exec-status until
// the process exits or agentExecDeadline elapses. A 403 anywhere names the
// VM.GuestAgent.Unrestricted privilege.
func (c *Client) AgentExec(node string, vmid int, argv []string, inputData []byte) (AgentExecResult, error) {
	if len(argv) == 0 {
		return AgentExecResult{}, fmt.Errorf("agent exec: empty command")
	}
	execPath := fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec", node, vmid)
	status, body, err := c.requestForm(http.MethodPost, execPath, agentCommandValues(argv, inputData))
	if err != nil {
		return AgentExecResult{}, err
	}
	if status == http.StatusForbidden {
		return AgentExecResult{}, fmt.Errorf("Proxmox denied guest-agent exec (403): %s", agentUnrestrictedHint())
	}
	if status >= 300 {
		return AgentExecResult{}, fmt.Errorf("proxmox API agent/exec: %d: %s", status, strings.TrimSpace(string(body)))
	}
	var execResp struct {
		Data struct {
			PID int `json:"pid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &execResp); err != nil {
		return AgentExecResult{}, fmt.Errorf("parsing agent/exec response: %w", err)
	}
	return c.pollAgentExecStatus(node, vmid, execResp.Data.PID)
}

// pollAgentExecStatus polls agent/exec-status for pid until the process exits or
// the deadline elapses, returning the finished result.
func (c *Client) pollAgentExecStatus(node string, vmid, pid int) (AgentExecResult, error) {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec-status?pid=%d", node, vmid, pid)
	deadline := time.Now().Add(agentExecDeadline)
	for {
		status, body, err := c.requestForm(http.MethodGet, path, nil)
		if err != nil {
			return AgentExecResult{}, err
		}
		if status == http.StatusForbidden {
			return AgentExecResult{}, fmt.Errorf("Proxmox denied guest-agent exec (403): %s", agentUnrestrictedHint())
		}
		if status >= 300 {
			return AgentExecResult{}, fmt.Errorf("proxmox API agent/exec-status: %d: %s", status, strings.TrimSpace(string(body)))
		}
		res, exited, err := parseAgentExecStatus(body)
		if err != nil {
			return AgentExecResult{}, err
		}
		if exited {
			return res, nil
		}
		if time.Now().After(deadline) {
			return AgentExecResult{}, fmt.Errorf("guest agent command did not finish within %s", agentExecDeadline)
		}
		time.Sleep(agentPollInterval)
	}
}

// GuestMem is a VM's memory usage read from INSIDE the guest via the QEMU guest
// agent (/proc/meminfo), so it reflects what the guest itself accounts as used
// rather than the hypervisor's balloon figure. The balloon figure Proxmox reports
// (and the dashboard mirrors) is MemTotal-MemFree, which counts reclaimable page
// cache as "used" and so runs to near-full on any healthy Linux guest; UsedMB here
// is MemTotal-MemAvailable, matching `free`/btop's "used". All fields in MB.
type GuestMem struct {
	TotalMB int // MemTotal
	UsedMB  int // MemTotal - MemAvailable (excludes reclaimable cache)
	AvailMB int // MemAvailable
	CacheMB int // Buffers + Cached + SReclaimable (the reclaimable slack)
}

// GuestMemory reads /proc/meminfo inside a running VM through the guest agent and
// returns the guest's own memory accounting. Needs qemu-guest-agent running and the
// VM.GuestAgent.Unrestricted privilege (same as AgentExec); any error means "no
// reading" and callers fall back to the balloon figure. VM-only — the agent path is
// /qemu/…; LXC has no QEMU agent.
func (c *Client) GuestMemory(node string, vmid int) (GuestMem, error) {
	res, err := c.AgentExec(node, vmid, []string{"cat", "/proc/meminfo"}, nil)
	if err != nil {
		return GuestMem{}, err
	}
	if res.ExitCode != 0 {
		return GuestMem{}, fmt.Errorf("reading /proc/meminfo via guest agent: exit %d: %s", res.ExitCode, strings.TrimSpace(res.ErrData))
	}
	return parseMeminfo(res.OutData)
}

// parseMeminfo turns the contents of /proc/meminfo into a GuestMem. It reads the kB
// values it needs and derives used as MemTotal-MemAvailable, so reclaimable page
// cache is NOT counted as used (matching `free`'s "available" column and btop).
// Kernels without MemAvailable (pre-3.14) fall back to MemFree+Buffers+Cached. Pure,
// so it is unit-tested directly.
func parseMeminfo(s string) (GuestMem, error) {
	kb := map[string]int{}
	for _, line := range strings.Split(s, "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:]) // "  6062116 kB" → ["6062116","kB"]
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		kb[strings.TrimSpace(line[:colon])] = n
	}
	total := kb["MemTotal"]
	if total <= 0 {
		return GuestMem{}, fmt.Errorf("meminfo: no MemTotal")
	}
	avail, ok := kb["MemAvailable"]
	if !ok {
		avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
	}
	used := total - avail
	if used < 0 {
		used = 0
	}
	toMB := func(v int) int { return v / 1024 }
	return GuestMem{
		TotalMB: toMB(total),
		UsedMB:  toMB(used),
		AvailMB: toMB(avail),
		CacheMB: toMB(kb["Buffers"] + kb["Cached"] + kb["SReclaimable"]),
	}, nil
}

// requestForm performs a form-encoded request and returns the status code and raw
// body without treating a non-2xx as a transport error, so callers can map codes
// (notably 403) to their own messages. Kept here since only the agent endpoints
// need to inspect the status code themselves.
func (c *Client) requestForm(method, path string, params url.Values) (int, []byte, error) {
	var body io.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", c.authHeader())
	if params != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// agentCommandValues encodes argv as the repeated `command=` form fields the PVE 8+
// agent/exec API expects (command as an array), plus an optional plain `input-data`
// field. Pure, so the encoding is unit-tested without a live API.
func agentCommandValues(argv []string, inputData []byte) url.Values {
	v := url.Values{}
	for _, a := range argv {
		v.Add("command", a)
	}
	if len(inputData) > 0 {
		v.Set("input-data", string(inputData))
	}
	return v
}

// parseAgentExecStatus decodes an agent/exec-status body into a result and whether
// the process has exited. `exited` is accepted as either the JSON number 1 or the
// boolean true (PVE serializes perl truthiness as 1, but be tolerant); out-data /
// err-data are run through decodeAgentData. Pure, so it is unit-tested directly.
func parseAgentExecStatus(body []byte) (AgentExecResult, bool, error) {
	var r struct {
		Data struct {
			Exited   json.RawMessage `json:"exited"`
			ExitCode int             `json:"exitcode"`
			OutData  string          `json:"out-data"`
			ErrData  string          `json:"err-data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return AgentExecResult{}, false, fmt.Errorf("parsing agent/exec-status: %w", err)
	}
	exited := false
	switch strings.TrimSpace(string(r.Data.Exited)) {
	case "1", "true":
		exited = true
	}
	return AgentExecResult{
		ExitCode: r.Data.ExitCode,
		OutData:  decodeAgentData(r.Data.OutData),
		ErrData:  decodeAgentData(r.Data.ErrData),
	}, exited, nil
}

// decodeAgentData returns the guest agent's out-data/err-data as plain text. The
// QEMU guest agent base64-encodes these fields, but some PVE builds decode them
// before returning, so the value may already be plain: try a strict base64 decode
// and fall back to the raw string when it isn't valid base64 (plain command output
// rarely is — it usually isn't a multiple of 4 and carries spaces/newlines). Pure,
// so it is unit-tested directly.
func decodeAgentData(s string) string {
	if s == "" {
		return ""
	}
	if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s)); err == nil {
		return string(dec)
	}
	return s
}
