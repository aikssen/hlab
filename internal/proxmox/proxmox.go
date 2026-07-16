// Package proxmox is a tiny client for the Proxmox VE API. hlab uses it to
// discover infrastructure (nodes, templates, storages, bridges) so the wizard
// can offer real choices, and for runtime power actions (start / stop / reboot)
// that are not part of a VM's declarative lifecycle.
//
// VM lifecycle mutations (create, destroy, config) go through Terraform, not
// this client; power state is runtime, so it lives here alongside discovery.
package proxmox

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client talks to the Proxmox API using an API token.
type Client struct {
	baseURL  string
	tokenID  string
	secret   string
	insecure bool // skip TLS verification; also honored by the console websocket dial
	http     *http.Client
}

// New builds a client. baseURL is e.g. "https://proxmox.example:8006/"; tokenID is e.g.
// "root@pam!hlab"; insecure skips TLS verification (common with self-signed PVE).
func New(baseURL, tokenID, secret string, insecure bool) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/api2/json") {
		baseURL += "/api2/json"
	}
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		baseURL:  baseURL,
		tokenID:  tokenID,
		secret:   secret,
		insecure: insecure,
		http:     &http.Client{Timeout: 15 * time.Second, Transport: tr},
	}
}

// authHeader returns the value for the Authorization header used on every
// request (REST and the console websocket): the API-token form Proxmox expects.
func (c *Client) authHeader() string {
	return fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.secret)
}

func (c *Client) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.secret))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxmox API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func (c *Client) post(path string) error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.secret))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// do performs an arbitrary method with an optional form body and returns the
// task UPID from the response (empty for endpoints that are not async). Used for
// the snapshot operations, which Proxmox runs as background tasks.
func (c *Client) do(method, path string, params url.Values) (string, error) {
	var body io.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.secret))
	if params != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("proxmox API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	var t struct {
		Data string `json:"data"` // the UPID for async node tasks
	}
	_ = json.Unmarshal(raw, &t)
	return t.Data, nil
}

// WaitTask polls a node task (by UPID) until it stops, returning an error if it
// finished with a non-OK exit status. A blank UPID (synchronous endpoint) is a
// no-op. Poll GETs are quick; the overall wait is bounded by timeout.
func (c *Client) WaitTask(node, upid string, timeout time.Duration) error {
	if upid == "" {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		var r struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := c.get(fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid)), &r); err != nil {
			return err
		}
		if r.Data.Status == "stopped" {
			if r.Data.ExitStatus != "" && r.Data.ExitStatus != "OK" {
				if log := c.taskLogTail(node, upid); log != "" {
					return fmt.Errorf("task failed: %s\n%s", r.Data.ExitStatus, log)
				}
				return fmt.Errorf("task failed: %s", r.Data.ExitStatus)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for task to finish")
		}
		time.Sleep(2 * time.Second)
	}
}

// taskLogTail fetches the last lines of a task's log, used to surface the real
// reason a task failed (the exit status alone is often just "migration aborted").
// Best-effort — returns "" on any error.
func (c *Client) taskLogTail(node, upid string) string {
	var r struct {
		Data []struct {
			N int    `json:"n"`
			T string `json:"t"`
		} `json:"data"`
	}
	// limit=100 caps the response; we keep the last few meaningful lines.
	if err := c.get(fmt.Sprintf("/nodes/%s/tasks/%s/log?limit=100", node, url.PathEscape(upid)), &r); err != nil {
		return ""
	}
	var lines []string
	for _, e := range r.Data {
		t := strings.TrimSpace(e.T)
		if t == "" || t == "TASK OK" {
			continue
		}
		lines = append(lines, "  "+t)
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 8 { // keep the tail — the failure and its context
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}

// guestPower posts a power action ("start"/"shutdown"/"stop"/"reboot") to a
// guest. kind is "qemu" or "lxc" — the API path differs between VMs and
// containers, so power is type-aware.
func (c *Client) guestPower(node, kind, action string, vmid int) error {
	return c.post(fmt.Sprintf("/nodes/%s/%s/%d/status/%s", node, kind, vmid, action))
}

// RebootVM requests a guest reboot (reboots cleanly via the guest agent/ACPI).
func (c *Client) RebootVM(node string, vmid int) error {
	return c.guestPower(node, "qemu", "reboot", vmid)
}

// StartVM powers on a stopped VM.
func (c *Client) StartVM(node string, vmid int) error {
	return c.guestPower(node, "qemu", "start", vmid)
}

// ShutdownVM requests a graceful guest shutdown (ACPI / guest agent). Proxmox
// queues the task and returns immediately; the guest powers off shortly after.
func (c *Client) ShutdownVM(node string, vmid int) error {
	return c.guestPower(node, "qemu", "shutdown", vmid)
}

// StopVM hard-stops a VM, cutting its power immediately (like pulling the plug).
// Prefer ShutdownVM unless the guest is unresponsive.
func (c *Client) StopVM(node string, vmid int) error {
	return c.guestPower(node, "qemu", "stop", vmid)
}

// StartGuest / ShutdownGuest / StopGuest / RebootGuest are the type-aware power
// actions used for discovered guests (VMs or LXC containers) that hlab does not
// manage; kind is "qemu" or "lxc".
func (c *Client) StartGuest(node, kind string, vmid int) error {
	return c.guestPower(node, kind, "start", vmid)
}

func (c *Client) ShutdownGuest(node, kind string, vmid int) error {
	return c.guestPower(node, kind, "shutdown", vmid)
}

func (c *Client) StopGuest(node, kind string, vmid int) error {
	return c.guestPower(node, kind, "stop", vmid)
}

func (c *Client) RebootGuest(node, kind string, vmid int) error {
	return c.guestPower(node, kind, "reboot", vmid)
}

// Guest is a running guest (VM or LXC container) as seen cluster-wide, used to
// surface resources that exist in Proxmox but are not managed by hlab.
type Guest struct {
	VMID   int
	Name   string
	Node   string
	Type   string // "qemu" or "lxc"
	Status string // "running" / "stopped" / …
	Cores  int
	MemMB  int // allocated memory (maxmem)
	DiskGB int

	// Template reports whether this is a template rather than a real guest —
	// used to exclude templates from adoption.
	Template bool

	// Live utilization, from the same cluster/resources call (no extra request).
	// Zero for a stopped guest.
	CPUFrac   float64 // fraction 0..1 of allocated CPU in use (×100 = percent)
	MemUsedMB int     // memory currently in use
	Uptime    int64   // seconds since boot

	// IP is filled in on demand (not part of the cluster/resources response): the
	// first non-loopback IPv4, resolved via the LXC interfaces or QEMU agent API.
	IP string
}

// ClusterGuests lists every VM and LXC container across the cluster in a single
// call (GET /cluster/resources?type=vm), with their node, type, power status and
// sizing. Used both to refresh managed VM statuses and to discover unmanaged
// guests. Read-only.
func (c *Client) ClusterGuests() ([]Guest, error) {
	var r struct {
		Data []struct {
			VMID     int     `json:"vmid"`
			Name     string  `json:"name"`
			Node     string  `json:"node"`
			Type     string  `json:"type"`
			Status   string  `json:"status"`
			MaxCPU   float64 `json:"maxcpu"`
			MaxMem   int64   `json:"maxmem"`
			MaxDisk  int64   `json:"maxdisk"`
			CPU      float64 `json:"cpu"`
			Mem      int64   `json:"mem"`
			Uptime   int64   `json:"uptime"`
			Template int     `json:"template"`
		} `json:"data"`
	}
	if err := c.get("/cluster/resources?type=vm", &r); err != nil {
		return nil, err
	}
	out := make([]Guest, 0, len(r.Data))
	for _, g := range r.Data {
		if g.Type != "qemu" && g.Type != "lxc" {
			continue // skip storage/node rows if the filter is ignored
		}
		out = append(out, Guest{
			VMID:      g.VMID,
			Name:      g.Name,
			Node:      g.Node,
			Type:      g.Type,
			Status:    g.Status,
			Cores:     int(g.MaxCPU),
			MemMB:     int(g.MaxMem / (1024 * 1024)),
			DiskGB:    int(g.MaxDisk / (1024 * 1024 * 1024)),
			Template:  g.Template == 1,
			CPUFrac:   g.CPU,
			MemUsedMB: int(g.Mem / (1024 * 1024)),
			Uptime:    g.Uptime,
		})
	}
	return out, nil
}

// NodeMetric is a Proxmox host node's live utilization, from /cluster/resources
// (type=node rows). Zero fields for an offline node.
type NodeMetric struct {
	Name       string
	Status     string  // "online" / "offline" / "unknown"
	CPUFrac    float64 // host CPU load, fraction 0..1 (×100 = percent)
	MemUsedMB  int64
	MemMaxMB   int64
	DiskUsedGB int64 // root filesystem
	DiskMaxGB  int64
	Uptime     int64 // seconds
}

// StorageMetric is a storage's capacity, from /cluster/resources (type=storage
// rows). Proxmox reports disk=used and maxdisk=total. A shared storage appears
// once per node in the response.
type StorageMetric struct {
	Name    string
	Node    string
	Status  string // "available" / …
	Type    string // plugin type (lvmthin, dir, zfspool, …)
	Content string // "images", "rootdir", "vztmpl,iso", …
	UsedGB  int64
	TotalGB int64
	Shared  bool
}

// ClusterMetricsData carries the fleet-wide node + storage metrics surfaced by
// the dashboard's metrics panel.
type ClusterMetricsData struct {
	Nodes   []NodeMetric
	Storage []StorageMetric
}

// resourceRow is one raw /cluster/resources row, covering the fields of the
// node/storage (and guest) row types. Only the subset used by ClusterMetrics is
// parsed.
type resourceRow struct {
	Type    string  `json:"type"` // "node" / "storage" / "qemu" / "lxc" / "sdn"
	Node    string  `json:"node"`
	Storage string  `json:"storage"`
	Status  string  `json:"status"`
	CPU     float64 `json:"cpu"`
	Mem     int64   `json:"mem"`
	MaxMem  int64   `json:"maxmem"`
	Disk    int64   `json:"disk"`    // used (bytes)
	MaxDisk int64   `json:"maxdisk"` // total (bytes)
	Uptime  int64   `json:"uptime"`
	Shared  int     `json:"shared"`
	Plugin  string  `json:"plugintype"`
	Content string  `json:"content"`
}

// ClusterMetrics returns host-node CPU/RAM/disk and per-storage capacity from the
// SAME /cluster/resources endpoint ClusterGuests uses — but WITHOUT the type=vm
// filter, so the node and storage rows are returned too. One read-only call.
func (c *Client) ClusterMetrics() (ClusterMetricsData, error) {
	var r struct {
		Data []resourceRow `json:"data"`
	}
	if err := c.get("/cluster/resources", &r); err != nil {
		return ClusterMetricsData{}, err
	}
	return parseClusterMetrics(r.Data), nil
}

// parseClusterMetrics converts raw /cluster/resources rows into node + storage
// metrics (guest rows are ignored — those come from ClusterGuests). Split out so
// it can be unit-tested without HTTP. Results are sorted by name for a stable
// dashboard render.
func parseClusterMetrics(rows []resourceRow) ClusterMetricsData {
	const gib = 1024 * 1024 * 1024
	const mib = 1024 * 1024
	var d ClusterMetricsData
	for _, row := range rows {
		switch row.Type {
		case "node":
			d.Nodes = append(d.Nodes, NodeMetric{
				Name:       row.Node,
				Status:     row.Status,
				CPUFrac:    row.CPU,
				MemUsedMB:  row.Mem / mib,
				MemMaxMB:   row.MaxMem / mib,
				DiskUsedGB: row.Disk / gib,
				DiskMaxGB:  row.MaxDisk / gib,
				Uptime:     row.Uptime,
			})
		case "storage":
			d.Storage = append(d.Storage, StorageMetric{
				Name:    row.Storage,
				Node:    row.Node,
				Status:  row.Status,
				Type:    row.Plugin,
				Content: row.Content,
				UsedGB:  row.Disk / gib,
				TotalGB: row.MaxDisk / gib,
				Shared:  row.Shared == 1,
			})
		}
	}
	sort.Slice(d.Nodes, func(i, j int) bool { return d.Nodes[i].Name < d.Nodes[j].Name })
	sort.Slice(d.Storage, func(i, j int) bool {
		if d.Storage[i].Node != d.Storage[j].Node {
			return d.Storage[i].Node < d.Storage[j].Node
		}
		return d.Storage[i].Name < d.Storage[j].Name
	})
	return d
}

// Snapshot is one saved VM snapshot.
type Snapshot struct {
	Name        string
	Description string
	Time        int64  // unix seconds the snapshot was taken (0 for the "current" node)
	Parent      string // the snapshot this one descends from
	WithRAM     bool   // the live memory state was captured (vmstate)
}

// Snapshots lists a guest's snapshots, newest first. kind is "qemu" or "lxc".
// The synthetic "current" entry (the running head, not a real snapshot) is
// filtered out.
func (c *Client) Snapshots(node, kind string, vmid int) ([]Snapshot, error) {
	var r struct {
		Data []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			SnapTime    int64  `json:"snaptime"`
			Parent      string `json:"parent"`
			VMState     int    `json:"vmstate"`
		} `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/%s/%d/snapshot", node, kind, vmid), &r); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(r.Data))
	for _, s := range r.Data {
		if s.Name == "current" {
			continue // the running head, not a real snapshot
		}
		out = append(out, Snapshot{
			Name:        s.Name,
			Description: s.Description,
			Time:        s.SnapTime,
			Parent:      s.Parent,
			WithRAM:     s.VMState == 1,
		})
	}
	// Newest first (the API returns them oldest-first / by parent chain).
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// CreateSnapshot snapshots a guest (kind "qemu"/"lxc"); withRAM captures the live
// memory state (only valid for a running VM — containers have no vmstate). Returns
// the task UPID to wait on.
func (c *Client) CreateSnapshot(node, kind string, vmid int, name, description string, withRAM bool) (string, error) {
	p := url.Values{}
	p.Set("snapname", name)
	if description != "" {
		p.Set("description", description)
	}
	if withRAM && kind == "qemu" {
		p.Set("vmstate", "1")
	}
	return c.do(http.MethodPost, fmt.Sprintf("/nodes/%s/%s/%d/snapshot", node, kind, vmid), p)
}

// RollbackSnapshot rolls a guest back to a snapshot, discarding changes made
// since. When start is true the guest is started after a successful rollback —
// needed because a snapshot without live memory state (all LXC snapshots, and VM
// snapshots taken without RAM) leaves the guest stopped otherwise. Returns the
// task UPID to wait on.
func (c *Client) RollbackSnapshot(node, kind string, vmid int, name string, start bool) (string, error) {
	p := url.Values{}
	if start {
		p.Set("start", "1")
	}
	return c.do(http.MethodPost, fmt.Sprintf("/nodes/%s/%s/%d/snapshot/%s/rollback", node, kind, vmid, url.PathEscape(name)), p)
}

// MigrateContainer moves an LXC container to another node via the Proxmox API
// (the bpg container resource has no migrate attribute, unlike the VM one). When
// restart is set, a running container is stopped, migrated and started again
// (containers can't live-migrate). Returns the task UPID to wait on.
func (c *Client) MigrateContainer(node string, vmid int, target string, restart bool) (string, error) {
	p := url.Values{}
	p.Set("target", target)
	if restart {
		p.Set("restart", "1")
	}
	return c.do(http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/migrate", node, vmid), p)
}

// GuestStatus returns a guest's current power status ("running"/"stopped"/…) via
// the per-guest status endpoint. kind is "qemu" or "lxc".
func (c *Client) GuestStatus(node, kind string, vmid int) (string, error) {
	var r struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/%s/%d/status/current", node, kind, vmid), &r); err != nil {
		return "", err
	}
	return r.Data.Status, nil
}

// DeleteSnapshot removes a snapshot. Returns the task UPID to wait on.
func (c *Client) DeleteSnapshot(node, kind string, vmid int, name string) (string, error) {
	return c.do(http.MethodDelete, fmt.Sprintf("/nodes/%s/%s/%d/snapshot/%s", node, kind, vmid, url.PathEscape(name)), nil)
}

// AgentIPv4s returns the non-loopback IPv4 addresses reported by the QEMU guest
// agent. An error usually means the agent is not ready yet.
func (c *Client) AgentIPv4s(node string, vmid int) ([]string, error) {
	var r struct {
		Data struct {
			Result []struct {
				Name        string `json:"name"`
				IPAddresses []struct {
					Type string `json:"ip-address-type"`
					Addr string `json:"ip-address"`
				} `json:"ip-addresses"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid), &r); err != nil {
		return nil, err
	}
	var ips []string
	for _, iface := range r.Data.Result {
		for _, a := range iface.IPAddresses {
			if a.Type == "ipv4" && !strings.HasPrefix(a.Addr, "127.") {
				ips = append(ips, a.Addr)
			}
		}
	}
	return ips, nil
}

// ContainerIPv4s returns the non-loopback IPv4 addresses of a running LXC
// container. Unlike a VM, a container needs no guest agent: the host reads the
// addresses directly from the container's network namespace via
// /nodes/{node}/lxc/{vmid}/interfaces (so this works for DHCP containers too).
// An error usually means the container is not running yet.
func (c *Client) ContainerIPv4s(node string, vmid int) ([]string, error) {
	var r struct {
		Data []struct {
			Name string `json:"name"`
			Inet string `json:"inet"` // e.g. "192.168.1.60/24"
		} `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/lxc/%d/interfaces", node, vmid), &r); err != nil {
		return nil, err
	}
	var ips []string
	for _, iface := range r.Data {
		if iface.Name == "lo" || iface.Inet == "" {
			continue
		}
		addr := iface.Inet
		if i := strings.IndexByte(addr, '/'); i >= 0 { // strip the CIDR suffix
			addr = addr[:i]
		}
		if strings.HasPrefix(addr, "127.") || addr == "" {
			continue
		}
		ips = append(ips, addr)
	}
	return ips, nil
}

// GuestIPv4 returns the first non-loopback IPv4 of a guest, routing by kind:
// containers read from the host namespace (no agent), VMs use the QEMU agent.
// Best-effort — returns "" when the address can't be determined (agent absent,
// guest stopped, …).
func (c *Client) GuestIPv4(node, kind string, vmid int) string {
	var (
		ips []string
		err error
	)
	if kind == "lxc" {
		ips, err = c.ContainerIPv4s(node, vmid)
	} else {
		ips, err = c.AgentIPv4s(node, vmid)
	}
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0]
}

// Ping verifies the credentials by hitting the version endpoint.
func (c *Client) Ping() error {
	var v struct {
		Data map[string]any `json:"data"`
	}
	return c.get("/version", &v)
}

// Version returns the Proxmox VE version string (e.g. "9.1.2") from the
// /version endpoint. Used to gate PVE 9.1+ behavior such as host-managed
// container networking.
func (c *Client) Version() (string, error) {
	var r struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := c.get("/version", &r); err != nil {
		return "", err
	}
	return r.Data.Version, nil
}

// NodeCPUVendor returns a node's host CPU vendor as Proxmox reports it —
// "AuthenticAMD", "GenuineIntel", or "" if it can't be read.
//
// It selects which CPU models a VM on that node could actually be given: a model
// is a promise about the instruction set the host will present, so an Intel model
// cannot start on an AMD host. Best-effort by design — the caller only uses it to
// narrow a list of choices, and an empty vendor just means offering the
// vendor-neutral ones.
func (c *Client) NodeCPUVendor(node string) string {
	var r struct {
		Data struct {
			CPUInfo struct {
				Vendor string `json:"vendor"`
			} `json:"cpuinfo"`
		} `json:"data"`
	}
	if err := c.get("/nodes/"+node+"/status", &r); err != nil {
		return ""
	}
	return r.Data.CPUInfo.Vendor
}

// Node is a cluster node.
type Node struct {
	Name   string `json:"node"`
	Status string `json:"status"`
}

// Nodes lists cluster nodes.
func (c *Client) Nodes() ([]Node, error) {
	var r struct {
		Data []Node `json:"data"`
	}
	if err := c.get("/nodes", &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Template is a VM template available on a node.
type Template struct {
	VMID int    `json:"vmid"`
	Name string `json:"name"`
	Node string `json:"-"`
}

// Templates lists VM templates on a node (qemu VMs with template=1).
func (c *Client) Templates(node string) ([]Template, error) {
	var r struct {
		Data []struct {
			VMID     int    `json:"vmid"`
			Name     string `json:"name"`
			Template int    `json:"template"`
		} `json:"data"`
	}
	if err := c.get("/nodes/"+node+"/qemu", &r); err != nil {
		return nil, err
	}
	var out []Template
	for _, v := range r.Data {
		if v.Template == 1 {
			out = append(out, Template{VMID: v.VMID, Name: v.Name, Node: node})
		}
	}
	return out, nil
}

// AllTemplates lists VM templates across every node in the cluster. A node that
// fails to query is skipped rather than failing the whole listing.
func (c *Client) AllTemplates() ([]Template, error) {
	nodes, err := c.Nodes()
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, n := range nodes {
		ts, err := c.Templates(n.Name)
		if err != nil {
			continue
		}
		out = append(out, ts...)
	}
	return out, nil
}

// Storage is a storage backend usable for VM disks.
type Storage struct {
	Name    string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Shared  int    `json:"shared"` // 1 when the storage is shared across nodes
}

// StorageShared reports whether a storage is shared across nodes. Non-shared
// (local) storage constrains operations like container migration with snapshots.
// Returns false when the storage can't be found.
func (c *Client) StorageShared(node, name string) (bool, error) {
	all, err := c.storagesRaw(node)
	if err != nil {
		return false, err
	}
	for _, s := range all {
		if s.Name == name {
			return s.Shared == 1, nil
		}
	}
	return false, nil
}

// storagesRaw lists every storage on a node, unfiltered.
func (c *Client) storagesRaw(node string) ([]Storage, error) {
	var r struct {
		Data []Storage `json:"data"`
	}
	if err := c.get("/nodes/"+node+"/storage", &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Storages lists storages on a node that can hold VM images.
func (c *Client) Storages(node string) ([]Storage, error) {
	return c.filterStorages(node, "images")
}

// ContainerStorages lists storages on a node that can hold LXC rootfs volumes.
func (c *Client) ContainerStorages(node string) ([]Storage, error) {
	return c.filterStorages(node, "rootdir")
}

func (c *Client) filterStorages(node, content string) ([]Storage, error) {
	all, err := c.storagesRaw(node)
	if err != nil {
		return nil, err
	}
	var out []Storage
	for _, s := range all {
		if strings.Contains(s.Content, content) {
			out = append(out, s)
		}
	}
	return out, nil
}

// ContainerTemplate is an LXC template (a vztmpl storage volume) available on a
// node, used as the base image when creating a container.
type ContainerTemplate struct {
	VolID string `json:"volid"` // e.g. local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst
	Name  string `json:"-"`     // basename for display
	Node  string `json:"-"`
}

// ContainerTemplates lists the vztmpl (container template) volumes across a node's
// storages.
func (c *Client) ContainerTemplates(node string) ([]ContainerTemplate, error) {
	stores, err := c.storagesRaw(node)
	if err != nil {
		return nil, err
	}
	var out []ContainerTemplate
	for _, s := range stores {
		if !strings.Contains(s.Content, "vztmpl") {
			continue
		}
		var r struct {
			Data []struct {
				VolID string `json:"volid"`
			} `json:"data"`
		}
		if err := c.get(fmt.Sprintf("/nodes/%s/storage/%s/content?content=vztmpl", node, s.Name), &r); err != nil {
			continue
		}
		for _, v := range r.Data {
			out = append(out, ContainerTemplate{VolID: v.VolID, Name: volidBase(v.VolID), Node: node})
		}
	}
	return out, nil
}

// AllContainerTemplates lists container templates across every node in the
// cluster. A node that fails to query is skipped rather than failing the listing.
func (c *Client) AllContainerTemplates() ([]ContainerTemplate, error) {
	nodes, err := c.Nodes()
	if err != nil {
		return nil, err
	}
	var out []ContainerTemplate
	for _, n := range nodes {
		ts, err := c.ContainerTemplates(n.Name)
		if err != nil {
			continue
		}
		out = append(out, ts...)
	}
	return out, nil
}

// volidBase returns the filename portion of a storage volume id, e.g.
// "local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst" -> "debian-12-standard_12.7-1_amd64.tar.zst".
func volidBase(volid string) string {
	if i := strings.LastIndex(volid, "/"); i >= 0 {
		return volid[i+1:]
	}
	return volid
}

// OSTypeFromTemplate infers the bpg operating_system.type from a container
// template name (its distro prefix). Defaults to "debian" when unknown, since the
// homelab standard base is Debian 12.
func OSTypeFromTemplate(name string) string {
	n := strings.ToLower(volidBase(name))
	for _, os := range []string{"ubuntu", "debian", "alpine", "centos", "fedora", "rockylinux", "archlinux", "opensuse", "devuan", "gentoo", "nixos"} {
		if strings.HasPrefix(n, os) {
			return os
		}
	}
	return "debian"
}

// TemplateDiskGB returns the size (in GiB, rounded up) of a VM's primary boot
// disk, used to default and lower-bound the disk size when cloning. Returns 0 if
// it cannot be determined.
func (c *Client) TemplateDiskGB(node string, vmid int) (int, error) {
	var r struct {
		Data map[string]any `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), &r); err != nil {
		return 0, err
	}
	_, spec := bootDiskSpec(r.Data)
	return diskSpecGB(spec), nil
}

// diskSpecGB extracts the size from a Proxmox disk spec such as
// "local-lvm:base-200-disk-0,iothread=1,size=32256M" and returns it in GiB.
func diskSpecGB(spec string) int {
	for part := range strings.SplitSeq(spec, ",") {
		if s, ok := strings.CutPrefix(part, "size="); ok {
			return sizeToGB(s)
		}
	}
	return 0
}

func sizeToGB(s string) int {
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'T', 't':
		mult, s = 1024, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1.0/1024, s[:len(s)-1]
	case 'K', 'k':
		mult, s = 1.0/(1024*1024), s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(math.Ceil(f * mult))
}

// Bridges lists Linux bridges on a node (network interfaces of type bridge).
func (c *Client) Bridges(node string) ([]string, error) {
	var r struct {
		Data []struct {
			Iface string `json:"iface"`
			Type  string `json:"type"`
		} `json:"data"`
	}
	if err := c.get("/nodes/"+node+"/network", &r); err != nil {
		return nil, err
	}
	var out []string
	for _, n := range r.Data {
		if n.Type == "bridge" {
			out = append(out, n.Iface)
		}
	}
	return out, nil
}
