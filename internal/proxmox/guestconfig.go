// Guest config readers: a normalized view of a live guest's Proxmox config,
// used by adopt to synthesize a declaration for a discovered (unmanaged) guest.
// Read-only, like the rest of discovery. (Package doc lives in proxmox.go.)

package proxmox

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GuestConfig is a normalized read of a live guest's Proxmox config
// (/nodes/{node}/qemu|lxc/{vmid}/config). Some fields only apply to one guest
// kind (noted per field); they are left at their zero value for the other kind.
type GuestConfig struct {
	Name string // qemu "name" / lxc "hostname"

	Cores   int
	Sockets int // qemu only; CPU sockets (hlab always declares 1)

	MemoryMB int
	SwapMB   int // lxc only

	DiskGB        int
	Storage       string // storage id the boot disk / rootfs lives on
	BootDiskIface string // qemu: e.g. "scsi0"; lxc: "rootfs"

	Bridge  string
	DHCP    bool
	IPCIDR  string
	Gateway string
	DNS     []string

	OSType       string // lxc only ("ostype")
	Unprivileged bool   // lxc only
	Nesting      bool   // lxc only ("features")
	HostManaged  bool   // lxc only; net0 "host-managed=1" (PVE 9.1+)

	CIUser  string   // qemu only ("ciuser")
	SSHKeys []string // qemu only ("sshkeys")

	Template     bool // the config-level template flag (mirrors Guest.Template)
	AgentEnabled bool // qemu only ("agent")

	// Extras hlab doesn't declare and can't safely adopt without losing them
	// (or forcing a replace). Populated with the raw config keys, e.g.
	// ["scsi1", "scsi2"] / ["net1"] / ["mp0"].
	ExtraDisks  []string // qemu: disk keys other than the boot disk
	ExtraNICs   []string // qemu + lxc: net1, net2, …
	MountPoints []string // lxc: mp0, mp1, …
}

// VMConfig reads a qemu VM's full config.
func (c *Client) VMConfig(node string, vmid int) (*GuestConfig, error) {
	var r struct {
		Data map[string]any `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), &r); err != nil {
		return nil, err
	}
	return parseVMConfig(r.Data), nil
}

// ContainerConfig reads an LXC container's full config.
func (c *Client) ContainerConfig(node string, vmid int) (*GuestConfig, error) {
	var r struct {
		Data map[string]any `json:"data"`
	}
	if err := c.get(fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), &r); err != nil {
		return nil, err
	}
	return parseContainerConfig(r.Data), nil
}

func parseVMConfig(cfg map[string]any) *GuestConfig {
	g := &GuestConfig{
		Name:     cfgStr(cfg, "name"),
		Cores:    cfgInt(cfg, "cores"),
		Sockets:  cfgInt(cfg, "sockets"),
		MemoryMB: cfgInt(cfg, "memory"),
		CIUser:   cfgStr(cfg, "ciuser"),
		Template: cfgInt(cfg, "template") == 1,
	}
	if g.Sockets == 0 {
		g.Sockets = 1 // Proxmox defaults to 1 socket when unset
	}

	iface, spec := bootDiskSpec(cfg)
	g.BootDiskIface = iface
	g.DiskGB = diskSpecGB(spec)
	g.Storage = diskStorage(spec)

	if net0, ok := cfg["net0"].(string); ok {
		g.Bridge = netField(net0, "bridge")
	}
	if agent, ok := cfg["agent"].(string); ok {
		g.AgentEnabled = agentEnabled(agent)
	}
	if ipc, ok := cfg["ipconfig0"].(string); ok {
		g.DHCP, g.IPCIDR, g.Gateway = parseIPConfig(ipc)
	} else {
		g.DHCP = true // no ipconfig0 at all behaves like DHCP for hlab's purposes
	}
	if ns, ok := cfg["nameserver"].(string); ok {
		g.DNS = strings.Fields(ns)
	}
	if sk, ok := cfg["sshkeys"].(string); ok {
		g.SSHKeys = parseSSHKeys(sk)
	}

	for k, v := range cfg {
		if k == iface || !isDiskKey(k) {
			continue
		}
		if s, ok := v.(string); ok && !strings.Contains(s, "cloudinit") {
			g.ExtraDisks = append(g.ExtraDisks, k)
		}
	}
	sort.Strings(g.ExtraDisks)
	g.ExtraNICs = extraNICKeys(cfg)

	return g
}

func parseContainerConfig(cfg map[string]any) *GuestConfig {
	g := &GuestConfig{
		Name:         cfgStr(cfg, "hostname"),
		Cores:        cfgInt(cfg, "cores"),
		MemoryMB:     cfgInt(cfg, "memory"),
		SwapMB:       cfgInt(cfg, "swap"),
		OSType:       cfgStr(cfg, "ostype"),
		Unprivileged: cfgInt(cfg, "unprivileged") == 1,
		Template:     cfgInt(cfg, "template") == 1,
	}

	if rootfs, ok := cfg["rootfs"].(string); ok {
		g.DiskGB = diskSpecGB(rootfs)
		g.Storage = diskStorage(rootfs)
		g.BootDiskIface = "rootfs"
	}

	if net0, ok := cfg["net0"].(string); ok {
		g.Bridge = netField(net0, "bridge")
		g.DHCP, g.IPCIDR, g.Gateway = parseIPConfig(net0)
		g.HostManaged = netField(net0, "host-managed") == "1"
	} else {
		g.DHCP = true
	}
	if ns, ok := cfg["nameserver"].(string); ok {
		g.DNS = strings.Fields(ns)
	}
	if feat, ok := cfg["features"].(string); ok {
		g.Nesting = featureFlag(feat, "nesting")
	}

	for k := range cfg {
		if isMountPointKey(k) {
			g.MountPoints = append(g.MountPoints, k)
		}
	}
	sort.Strings(g.MountPoints)
	g.ExtraNICs = extraNICKeys(cfg)

	return g
}

// bootDiskSpec finds a qemu VM's boot disk in its /config map: it prefers the
// configured boot order, then falls back to the conventional interface names,
// skipping the cloud-init drive. Shared by TemplateDiskGB (disk size only) and
// parseVMConfig (size + storage + interface name). Returns ("", "") if no boot
// disk can be identified.
func bootDiskSpec(cfg map[string]any) (iface, spec string) {
	var keys []string
	if b, ok := cfg["boot"].(string); ok {
		for _, d := range strings.Split(strings.TrimPrefix(b, "order="), ";") {
			if d != "" {
				keys = append(keys, d)
			}
		}
	}
	keys = append(keys, "scsi0", "virtio0", "sata0", "ide0")
	for _, k := range keys {
		v, ok := cfg[k].(string)
		if !ok || strings.Contains(v, "cloudinit") {
			continue
		}
		return k, v
	}
	return "", ""
}

// diskStorage extracts the storage id from a Proxmox disk spec such as
// "local-lvm:base-200-disk-0,iothread=1,size=32256M" -> "local-lvm".
func diskStorage(spec string) string {
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		return spec[:i]
	}
	return ""
}

// netField extracts one comma-separated key=value field from a Proxmox net/ip
// spec, e.g. netField("virtio=AA:BB,bridge=vmbr0,firewall=1", "bridge") -> "vmbr0".
func netField(spec, key string) string {
	for _, part := range strings.Split(spec, ",") {
		if v, ok := strings.CutPrefix(part, key+"="); ok {
			return v
		}
	}
	return ""
}

// parseIPConfig reads ip=/gw= out of a qemu ipconfig0 value or an lxc net0
// value (both use the same key=value,… shape for these two keys). "ip=dhcp" or
// a missing ip= is reported as DHCP.
func parseIPConfig(spec string) (dhcp bool, cidr, gateway string) {
	ip := netField(spec, "ip")
	if ip == "" || ip == "dhcp" {
		return true, "", ""
	}
	return false, ip, netField(spec, "gw")
}

// agentEnabled parses a qemu "agent" config value, which is either a bare
// "0"/"1", a comma-separated list starting with the enabled flag (e.g.
// "1,fstrim_cloned_disks=1"), or an explicit "enabled=0/1" field.
func agentEnabled(s string) bool {
	parts := strings.Split(s, ",")
	if len(parts) > 0 && parts[0] == "1" {
		return true
	}
	for _, p := range parts {
		if v, ok := strings.CutPrefix(p, "enabled="); ok {
			return v == "1"
		}
	}
	return false
}

// featureFlag reads a boolean flag out of a Proxmox "features" config value
// (e.g. "nesting=1,keyctl=1").
func featureFlag(s, key string) bool {
	for _, part := range strings.Split(s, ",") {
		if v, ok := strings.CutPrefix(part, key+"="); ok {
			return v == "1"
		}
	}
	return false
}

// parseSSHKeys decodes a qemu "sshkeys" config value: URL-encoded, newline
// (%0A) separated public keys. Deliberately uses url.PathUnescape rather than
// url.QueryUnescape — the latter turns '+' into a space, which corrupts the
// base64 key material (ssh-rsa/ssh-ed25519 keys routinely contain '+').
func parseSSHKeys(s string) []string {
	decoded, err := url.PathUnescape(s)
	if err != nil {
		decoded = s
	}
	var keys []string
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			keys = append(keys, line)
		}
	}
	return keys
}

var (
	diskKeyRe = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d+$`)
	netKeyRe  = regexp.MustCompile(`^net\d+$`)
	mpKeyRe   = regexp.MustCompile(`^mp\d+$`)
)

func isDiskKey(k string) bool       { return diskKeyRe.MatchString(k) }
func isNetKey(k string) bool        { return netKeyRe.MatchString(k) }
func isMountPointKey(k string) bool { return mpKeyRe.MatchString(k) }

// extraNICKeys returns every netN key besides net0 (both qemu and lxc configs
// use the same netN naming for additional interfaces).
func extraNICKeys(cfg map[string]any) []string {
	var out []string
	for k := range cfg {
		if k != "net0" && isNetKey(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// cfgStr / cfgInt read a value out of a Proxmox /config map defensively: PVE
// returns numeric fields as either a JSON number or a numeric string depending
// on version.
func cfgStr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func cfgInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}
