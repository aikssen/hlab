// Package tui implements hlab's full-screen dashboard (milestone M3): a
// persistent terminal UI that lists the managed VMs and runs the create /
// provision / setup / destroy flows over internal/engine — the same
// orchestration the CLI uses. It is a thin view layer; all state and side
// effects live in the engine and the packages beneath it.
//
// Interaction stays inside the dashboard, rendered as modal windows floating
// over the VM table: the create / provision / setup / destroy forms are huh
// forms embedded as a child model (no terminal handoff), and long operations
// (terraform apply, ansible) show a fixed-size progress window with an animated
// bar and a toggleable output box. Only ssh leaves the dashboard (it is an
// external interactive process), via a brief suspend/resume.
package tui

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
	"github.com/aikssen/hlab/internal/terraform"
	"github.com/aikssen/hlab/internal/theme"
)

// program is the running dashboard, kept package-level so streaming actions can
// push log lines back via program.Send and ssh can suspend/resume the terminal.
var program *tea.Program

type uiMode int

const (
	modeDash    uiMode = iota // the VM table
	modeForm                  // an embedded huh form is active
	modeRun                   // a streaming operation is live (log panel)
	modeHelp                  // the keybinding help overlay
	modeConfirm               // a yes/no confirmation modal (discovered power actions)
	modeSnaps                 // the snapshots browser for a managed VM
)

type formKind int

const (
	fkCreate formKind = iota
	fkProvision
	fkDestroy
	fkMigrate
	fkEdit
	fkSnapshot
	fkSetup
	fkAdopt
	fkTheme
	fkInject
)

const (
	outputW = 54 // output viewport content width
	outputH = 12 // output viewport height (rows)
)

// Model is the dashboard state.
type Model struct {
	eng *engine.Engine
	pm  *proxmox.Client // concrete client for form-building/discovery (Nodes,
	//                         Templates, Storages, Bridges, GuestIPv4…); the engine
	//                         holds only the narrow engine.Proxmox interface
	version string // build version for the title bar (e.g. "v0.7.1-1a2b3c4")

	vms      []*state.VMSpec          // managed VMs (from homelab-state)
	guests   []proxmox.Guest          // discovered guests (in Proxmox, not managed by hlab)
	statuses map[string]string        // managed VM name → power status
	live     map[string]proxmox.Guest // managed VM name → live utilization (CPU/RAM/uptime)
	ips      map[string][]string
	metrics  proxmox.ClusterMetricsData // fleet-wide node CPU/RAM + storage (global metrics panel)
	cursor   int                        // single selection index over managed rows then discovered rows
	width    int
	height   int

	mode uiMode

	// confirm modal (discovered power actions)
	confirmPrompt string
	confirmCmd    tea.Cmd

	// embedded form + its context
	form        *huh.Form
	formKind    formKind
	formTitle   string
	createB     *createBinding
	provB       *provBinding
	provVM      *state.VMSpec
	destroyB    *destroyBinding
	destroyName string
	migrateB    *migrateBinding
	migrateVM   *state.VMSpec
	editB       *editBinding
	editVM      *state.VMSpec
	snapB       *snapBinding
	setupB      *setupBinding
	adoptB      *adoptBinding
	themeB      *themeBinding
	injectB     *injectBinding
	injectVM    *state.VMSpec

	// snapshots browser (modeSnaps)
	snapVM     *state.VMSpec
	snaps      []proxmox.Snapshot
	snapCursor int

	// streaming exec state
	runTitle  string
	logLines  []string
	runFrame  int                // progress-bar animation counter
	showLog   bool               // reveal the fixed-size output box during a run
	logVP     viewport.Model     // scrollable output box
	follow    bool               // auto-scroll the output box to the newest line
	cancel    context.CancelFunc // cancels the in-flight operation
	cancelled bool               // the user requested cancellation

	status string // last result / hint (footer)
	err    error  // load/discovery error (auto-cleared on a successful refresh)
	opErr  error  // last operation error; persists until the next op or manual refresh

	// drift is the last whole-fleet drift check (key P), keyed by managed VM
	// name. Populated on-demand only — never by the periodic refresh. Entries
	// for VMs that no longer exist are pruned on the next loadedMsg.
	drift        map[string]engine.DriftStatus
	driftSummary string

	// realMem is the guest's own memory accounting (guest-agent /proc/meminfo) for
	// running managed VMs, keyed by name. The detail panel prefers this over the
	// balloon figure Proxmox reports (which counts reclaimable page cache as used).
	// Best-effort and last-good: a transient agent failure keeps the prior reading.
	realMem map[string]proxmox.GuestMem
}

// loadedMsg carries a fresh snapshot: managed VMs (IPs + power status) and the
// guests discovered in Proxmox that hlab does not manage.
type loadedMsg struct {
	vms        []*state.VMSpec
	ips        map[string][]string
	statuses   map[string]string        // managed vm name → "running"/"stopped"/…
	live       map[string]proxmox.Guest // managed vm name → live utilization
	discovered []proxmox.Guest
	err        error
}

// New builds the dashboard over the given engine. version is the build version
// shown in the title bar (same string as `hlab version`, minus the "hlab "
// prefix); empty hides it.
func New(eng *engine.Engine, pm *proxmox.Client, version string) Model {
	// Seed the managed table from local state (a fast YAML read) so the rows are
	// present — and navigable — from the very first frame. The async refresh()
	// still runs on Init to fill in live status / discovered guests, but on a fresh
	// first launch that call waits on the (cold) Proxmox API; without the seed the
	// table would be empty until it returns, so ↑/↓ would appear dead for seconds.
	vms, _ := eng.Store.List()
	// Apply the configured color theme (falls back to default on unknown/empty)
	// before the first frame renders.
	initStyles(theme.Get(eng.Cfg.Theme))
	return Model{eng: eng, pm: pm, version: version, vms: vms}
}

// Run launches the dashboard program (alternate screen). pm is the concrete
// Proxmox client used for form-building/discovery (the engine wraps it as the
// narrow engine.Proxmox interface).
func Run(eng *engine.Engine, pm *proxmox.Client, version string) error {
	program = tea.NewProgram(New(eng, pm, version), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.fetchMetrics(), refreshTickCmd())
}

// refresh reloads the managed VMs, their IPs, and one cluster-wide guest list
// (a single API call) from which it derives both the managed power statuses and
// the discovered (unmanaged) guests. Runs off the UI thread.
func (m Model) refresh() tea.Cmd {
	// Snapshot the store/runner/PM on the UI goroutine before spawning the worker.
	// reloadEngine (after the Setup form) swaps m.eng's Runner/PM for fresh values
	// on this same goroutine; reading them inside the worker would race with that.
	// Capturing here means an in-flight refresh keeps using the stack it started
	// with, and the next tick picks up the reloaded one.
	store := m.eng.Store
	runner := m.eng.Runner
	pm := m.pm
	return func() tea.Msg {
		vms, err := store.List()
		if err != nil {
			return loadedMsg{err: err}
		}
		ips := runner.IPAddresses()

		guests, gerr := pm.ClusterGuests()
		if gerr != nil {
			// Degrade gracefully: still show the managed VMs without live status
			// or the discovered section if the cluster query fails.
			return loadedMsg{vms: vms, ips: ips}
		}

		managed := make(map[int]string, len(vms)) // vmid → managed name
		for _, vm := range vms {
			managed[vm.VMID] = vm.Name
		}
		statuses := make(map[string]string, len(vms))
		live := make(map[string]proxmox.Guest, len(vms))
		var discovered []proxmox.Guest
		for _, g := range guests {
			if name, ok := managed[g.VMID]; ok {
				statuses[name] = g.Status
				live[name] = g
			} else {
				discovered = append(discovered, g)
			}
		}
		sort.Slice(discovered, func(i, j int) bool { return discovered[i].VMID < discovered[j].VMID })

		return loadedMsg{vms: vms, ips: ips, statuses: statuses, live: live, discovered: discovered}
	}
}

// guestIPsMsg carries the per-guest IP lookups resolved after a snapshot: IPs
// for managed DHCP containers (name → addrs) and for running discovered guests
// (vmid → ip). Merged into the model when it arrives.
type guestIPsMsg struct {
	lxc  map[string][]string
	disc map[int]string
}

// metricsMsg carries the fleet-wide node + storage metrics for the global metrics
// panel, fetched alongside each refresh.
type metricsMsg struct {
	data proxmox.ClusterMetricsData
	err  error
}

// fetchMetrics loads the cluster-wide node CPU/RAM + storage metrics (one
// read-only /cluster/resources call) for the metrics panel. Like resolveGuestIPs
// it snapshots m.pm on the UI goroutine and runs off the paint; a failure is kept
// silent (the panel shows its last-good/loading state) so it never blanks the
// dashboard's error line.
func (m Model) fetchMetrics() tea.Cmd {
	pm := m.pm
	return func() tea.Msg {
		if pm == nil {
			return metricsMsg{}
		}
		d, err := pm.ClusterMetrics()
		return metricsMsg{data: d, err: err}
	}
}

// resolveGuestIPs looks up the IPs the cluster snapshot can't provide: managed
// DHCP containers (host interfaces API) and running discovered guests (host or
// guest agent). These are one API call per guest — and the agent call blocks
// for a few seconds on a VM without the agent running — so they are resolved
// concurrently and OFF the first paint: refresh() returns immediately and this
// follow-up message fills the IP column when ready. Best-effort throughout.
func (m Model) resolveGuestIPs(vms []*state.VMSpec, statuses map[string]string, discovered []proxmox.Guest) tea.Cmd {
	// Snapshot the discovery client on the UI goroutine. reloadEngine (after the
	// Setup form) reassigns m.pm on the UI thread; reading it inside the worker
	// goroutines below would race with that write. Capturing the pointer here is a
	// consistent snapshot — an in-flight resolve keeps using the client it started
	// with, and the next refresh picks up the new one.
	pm := m.pm
	return func() tea.Msg {
		msg := guestIPsMsg{lxc: map[string][]string{}, disc: map[int]string{}}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // be polite to the Proxmox API

		// LXC containers have no QEMU agent, so their IP isn't in the terraform
		// output. Discover it from the host (interfaces API) for running containers
		// without a declared static IP, so the list shows an address for DHCP LXC too.
		for _, vm := range vms {
			if !vm.IsLXC() || engine.DeclaredIP(vm) != "" || statuses[vm.Name] != "running" {
				continue
			}
			wg.Add(1)
			go func(vm *state.VMSpec) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if addrs, aerr := pm.ContainerIPv4s(vm.Node, vm.VMID); aerr == nil && len(addrs) > 0 {
					mu.Lock()
					msg.lxc[vm.Name] = addrs
					mu.Unlock()
				}
			}(vm)
		}

		// Discovered (unmanaged) guests — LXC from the host, VMs from the agent.
		for _, g := range discovered {
			if g.Status != "running" {
				continue
			}
			wg.Add(1)
			go func(g proxmox.Guest) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ip := pm.GuestIPv4(g.Node, g.Type, g.VMID); ip != "" {
					mu.Lock()
					msg.disc[g.VMID] = ip
					mu.Unlock()
				}
			}(g)
		}

		wg.Wait()
		return msg
	}
}

// guestMemMsg carries the real (guest-agent) memory readings for running managed
// VMs, keyed by name, resolved off-paint after a snapshot.
type guestMemMsg struct {
	mem map[string]proxmox.GuestMem
}

// resolveGuestMem reads real memory usage from inside each running managed VM via
// the QEMU guest agent, concurrently and off-paint (best-effort, like
// resolveGuestIPs). Containers are skipped — they have no QEMU agent — so the
// detail panel keeps the balloon figure for LXC. A per-VM failure just omits that
// VM from the result; the model keeps its last-good reading.
func (m Model) resolveGuestMem(vms []*state.VMSpec, statuses map[string]string) tea.Cmd {
	pm := m.pm
	return func() tea.Msg {
		out := map[string]proxmox.GuestMem{}
		if pm == nil {
			return guestMemMsg{mem: out}
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // be polite to the Proxmox API
		for _, vm := range vms {
			if vm.IsLXC() || statuses[vm.Name] != "running" {
				continue
			}
			wg.Add(1)
			go func(vm *state.VMSpec) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if gm, err := pm.GuestMemory(vm.Node, vm.VMID); err == nil && gm.TotalMB > 0 {
					mu.Lock()
					out[vm.Name] = gm
					mu.Unlock()
				}
			}(vm)
		}
		wg.Wait()
		return guestMemMsg{mem: out}
	}
}

// refreshTickMsg drives the periodic dashboard refresh.
type refreshTickMsg struct{}

func refreshTickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeForm && m.form != nil {
			f, cmd := m.form.Update(msg)
			m.form = asForm(f, m.form)
			return m, cmd
		}
		return m, nil

	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		// Carry the previously resolved per-guest IPs into the fresh snapshot so
		// the IP column doesn't blank out while resolveGuestIPs runs again.
		prevDisc := make(map[int]string, len(m.guests))
		for _, g := range m.guests {
			if g.IP != "" {
				prevDisc[g.VMID] = g.IP
			}
		}
		for i := range msg.discovered {
			if msg.discovered[i].Status == "running" {
				msg.discovered[i].IP = prevDisc[msg.discovered[i].VMID]
			}
		}
		for name, addrs := range m.ips {
			if _, ok := msg.ips[name]; !ok {
				msg.ips[name] = addrs
			}
		}
		m.vms = msg.vms
		m.statuses = msg.statuses
		m.live = msg.live
		m.guests = msg.discovered
		m.ips = msg.ips
		// Drift is never recomputed automatically — only prune entries for guests
		// that no longer exist, so a removed guest's stale drift doesn't linger.
		if m.drift != nil {
			current := make(map[string]bool, len(msg.vms))
			for _, vm := range msg.vms {
				current[vm.Name] = true
			}
			for name := range m.drift {
				if !current[name] {
					delete(m.drift, name)
				}
			}
		}
		if n := m.rowCount(); m.cursor >= n {
			m.cursor = max(n-1, 0)
		}
		return m, tea.Batch(
			m.resolveGuestIPs(msg.vms, msg.statuses, msg.discovered),
			m.resolveGuestMem(msg.vms, msg.statuses),
			m.fetchMetrics(),
		)

	case guestMemMsg:
		if m.realMem == nil {
			m.realMem = map[string]proxmox.GuestMem{}
		}
		// Merge (last-good): a VM missing from this round keeps its prior reading, so
		// a transient agent blip doesn't flip the gauge back to the balloon figure.
		maps.Copy(m.realMem, msg.mem)
		// Prune readings for VMs that no longer exist so the map can't grow unbounded.
		if len(m.realMem) > 0 {
			current := make(map[string]bool, len(m.vms))
			for _, vm := range m.vms {
				current[vm.Name] = true
			}
			for name := range m.realMem {
				if !current[name] {
					delete(m.realMem, name)
				}
			}
		}
		return m, nil

	case guestIPsMsg:
		maps.Copy(m.ips, msg.lxc)
		for i := range m.guests {
			if ip, ok := msg.disc[m.guests[i].VMID]; ok {
				m.guests[i].IP = ip
			}
		}
		return m, nil

	case metricsMsg:
		// Silent on error: keep the last-good metrics so a transient blip doesn't
		// blank the panel or the dashboard's error line.
		if msg.err == nil {
			m.metrics = msg.data
		}
		return m, nil

	case refreshTickMsg:
		// Auto-refresh only on the dashboard, so it never disturbs an open modal
		// or a streaming operation. Keep the ticker alive regardless.
		if m.mode == modeDash {
			return m, tea.Batch(m.refresh(), refreshTickCmd())
		}
		return m, refreshTickCmd()

	case logLineMsg:
		m.logLines = append(m.logLines, stripANSI(string(msg)))
		if n := len(m.logLines); n > 1000 {
			m.logLines = m.logLines[n-1000:]
		}
		m.syncLogViewport()
		return m, nil

	case runDoneMsg:
		return m.onRunDone(msg)

	case driftDoneMsg:
		return m.onDriftDone(msg)

	case snapsLoadedMsg:
		if msg.err != nil {
			m.err, m.mode = msg.err, modeDash
			return m, nil
		}
		m.snapVM, m.snaps = msg.vm, msg.snaps
		if m.snapCursor >= len(m.snaps) {
			m.snapCursor = max(len(m.snaps)-1, 0)
		}
		m.mode, m.status, m.err = modeSnaps, "", nil
		return m, nil

	case runOpMsg:
		return m.startRun(msg.title, msg.cmd)

	case tickMsg:
		// Stop animating when the run ends — either because the window closed, or
		// because it failed and is being held open for the operator to read.
		if m.mode != modeRun || m.opErr != nil {
			return m, nil
		}
		m.runFrame++
		return m, tickCmd()
	}

	switch m.mode {
	case modeForm:
		// Esc closes the whole modal (huh doesn't abort on Esc by default).
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			m.mode, m.form, m.status = modeDash, nil, "cancelled"
			return m, m.refresh()
		}
		f, cmd := m.form.Update(msg)
		m.form = asForm(f, m.form)
		if m.form.State != huh.StateNormal {
			return m.onFormDone()
		}
		return m, cmd
	case modeRun:
		km, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		switch km.String() {
		case "l":
			m.showLog = !m.showLog
			return m, nil
		case "ctrl+c", "esc":
			// A failed run is already over — there is nothing left to cancel, so esc
			// dismisses the window the operator was kept in to read the output.
			if m.opErr != nil {
				m.mode = modeDash
				return m, nil
			}
			if m.cancel != nil && !m.cancelled {
				m.cancelled = true
				m.runTitle = "Cancelling " + strings.TrimSuffix(m.runTitle, "…")
				m.cancel()
			}
			return m, nil
		case "up", "down", "k", "j", "pgup", "pgdown", "home", "end":
			if m.showLog {
				var cmd tea.Cmd
				m.logVP, cmd = m.logVP.Update(msg)
				m.follow = m.logVP.AtBottom()
				return m, cmd
			}
		}
		return m, nil

	case modeHelp:
		if _, ok := msg.(tea.KeyMsg); ok {
			m.mode = modeDash
		}
		return m, nil

	case modeConfirm:
		km, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		switch km.String() {
		case "y", "Y", "enter":
			cmd := m.confirmCmd
			m.mode, m.confirmCmd, m.confirmPrompt = modeDash, nil, ""
			return m, cmd
		case "n", "N", "esc", "ctrl+c":
			m.mode, m.confirmCmd, m.confirmPrompt = modeDash, nil, ""
			m.status = "cancelled"
			return m, nil
		}
		return m, nil

	case modeSnaps:
		km, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		return m.handleSnapsKey(km)
	}

	// modeDash
	if km, ok := msg.(tea.KeyMsg); ok {
		return m.handleDashKey(km)
	}
	return m, nil
}

func (m Model) handleDashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys (not tied to the selection).
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < m.rowCount()-1 {
			m.cursor++
		}
		return m, nil
	case "R":
		m.status, m.opErr = "", nil
		return m, m.refresh()
	case "n":
		return m.enterCreateForm()
	case "S":
		return m.enterSetupForm()
	case "t":
		return m.enterThemeForm()
	case "P":
		return m.startRun("Detecting drift (terraform plan, whole fleet)", m.streamPlan())
	case "?":
		m.mode = modeHelp
		return m, nil
	}

	// Selection-dependent keys. A managed VM has the full set; a discovered
	// (unmanaged) guest only has power/reboot, both gated by a confirmation.
	if vm := m.selectedVM(); vm != nil {
		switch msg.String() {
		case "p":
			return m.enterProvisionForm(vm)
		case "s":
			return m, m.startSSH(vm)
		case "b":
			return m.togglePower(vm)
		case "r":
			return m.requestReboot(vm)
		case "d":
			return m.enterDestroyForm(vm)
		case "m":
			return m.enterMigrateForm(vm)
		case "e":
			return m.enterEditForm(vm)
		case "v":
			m.status = "loading snapshots…"
			return m, m.loadSnaps(vm)
		case "i":
			return m.enterInjectForm(vm)
		case "u":
			return m.askConfirm(fmt.Sprintf("Re-provision %s? Re-runs Ansible with its saved selection.", vm.Name),
				wrapRunOp("Updating "+vm.Name, m.streamUpdate(vm, false)))
		case "U":
			return m.askConfirm(fmt.Sprintf("Update + UPGRADE %s? apt upgrade + mise/runtime upgrades + CLI self-update.", vm.Name),
				wrapRunOp("Upgrading "+vm.Name, m.streamUpdate(vm, true)))
		}
		return m, nil
	}
	if g := m.selectedGuest(); g != nil {
		switch msg.String() {
		case "a":
			return m.enterAdoptForm(*g)
		case "b":
			if g.Status == "running" {
				return m.askConfirm(fmt.Sprintf("Shut down %s (%s)?", g.Name, g.Type), m.stopGuest(*g))
			}
			m.status = "starting " + g.Name + "…"
			return m, m.startGuest(*g)
		case "r":
			if g.Status == "running" {
				return m.askConfirm(fmt.Sprintf("Reboot %s (%s)?", g.Name, g.Type), m.rebootGuest(*g))
			}
			m.status = g.Name + " is stopped — press b to start it"
			return m, nil
		}
	}
	return m, nil
}

// handleSnapsKey drives the snapshots browser: navigate the list, create a new
// snapshot, or roll back / delete the selected one (both behind a confirmation).
func (m Model) handleSnapsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeDash
		return m, m.refresh()
	case "up", "k":
		if m.snapCursor > 0 {
			m.snapCursor--
		}
		return m, nil
	case "down", "j":
		if m.snapCursor < len(m.snaps)-1 {
			m.snapCursor++
		}
		return m, nil
	case "c":
		return m.enterSnapshotForm(m.snapVM)
	case "r", "enter":
		s := m.selectedSnap()
		if s == nil {
			return m, nil
		}
		return m.askConfirm(
			fmt.Sprintf("Roll back %s to %q? Changes since it are lost.", m.snapVM.Name, s.Name),
			wrapRunOp("Rolling back "+m.snapVM.Name, m.streamSnapshotRollback(m.snapVM.Name, s.Name)),
		)
	case "d":
		s := m.selectedSnap()
		if s == nil {
			return m, nil
		}
		return m.askConfirm(
			fmt.Sprintf("Delete snapshot %q of %s?", s.Name, m.snapVM.Name),
			wrapRunOp("Deleting snapshot "+s.Name, m.streamSnapshotDelete(m.snapVM.Name, s.Name)),
		)
	}
	return m, nil
}

// selectedSnap returns the snapshot under the browser cursor, or nil if empty.
func (m Model) selectedSnap() *proxmox.Snapshot {
	if m.snapCursor >= 0 && m.snapCursor < len(m.snaps) {
		return &m.snaps[m.snapCursor]
	}
	return nil
}

// askConfirm opens a yes/no modal that runs action when confirmed.
func (m Model) askConfirm(prompt string, action tea.Cmd) (tea.Model, tea.Cmd) {
	m.mode = modeConfirm
	m.confirmPrompt = prompt
	m.confirmCmd = action
	return m, nil
}

// rowCount is the number of selectable rows (managed VMs then discovered guests).
func (m Model) rowCount() int { return len(m.vms) + len(m.guests) }

// togglePower starts a stopped VM or gracefully shuts down a running one, based
// on its last-known power status. The call is quick (Proxmox queues the task and
// returns), so it runs without the streaming run modal; the periodic refresh
// reflects the new state once the guest settles.
func (m Model) togglePower(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	if m.statuses[vm.Name] == "running" {
		m.status = "stopping " + vm.Name + "…"
		return m, m.powerOff(vm)
	}
	m.status = "starting " + vm.Name + "…"
	return m, m.powerOn(vm)
}

// requestReboot gracefully reboots the selected VM. A stopped VM cannot reboot,
// so it hints to start it instead; otherwise the periodic refresh reflects the
// brief power cycle.
func (m Model) requestReboot(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	if m.statuses[vm.Name] == "stopped" {
		m.status = vm.Name + " is stopped — press b to start it"
		return m, nil
	}
	m.status = "rebooting " + vm.Name + "…"
	return m, m.rebootVM(vm)
}

// --- entering embedded forms ---

func (m Model) enterCreateForm() (tea.Model, tea.Cmd) {
	used := map[string]bool{}
	if existing, lerr := m.eng.Store.List(); lerr == nil {
		for _, vm := range existing {
			if vm.IPCIDR != "" {
				used[strings.SplitN(vm.IPCIDR, "/", 2)[0]] = true
			}
		}
	}
	b, err := newCreateBinding(m.eng.Cfg, m.pm, m.eng.Cfg.SuggestIPCIDR(used))
	if err != nil {
		m.err = err
		return m, nil
	}
	m.createB, m.form, m.formKind = b, b.form, fkCreate
	m.formTitle = "New VM"
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterProvisionForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	b, err := newProvBinding(vm, m.eng.Cfg.DotfilesRepo != "")
	if err != nil {
		m.err = err
		return m, nil
	}
	m.provB, m.provVM, m.form, m.formKind = b, vm, b.form, fkProvision
	m.formTitle = "Provision " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterDestroyForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	b := newDestroyBinding(vm.Name)
	m.destroyB, m.destroyName, m.form, m.formKind = b, vm.Name, b.form, fkDestroy
	m.formTitle = "Destroy " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterMigrateForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	b, err := newMigrateBinding(vm, m.pm)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.migrateB, m.migrateVM, m.form, m.formKind = b, vm, b.form, fkMigrate
	m.formTitle = "Migrate " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterEditForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	b := newEditBinding(vm)
	m.editB, m.editVM, m.form, m.formKind = b, vm, b.form, fkEdit
	m.formTitle = "Edit " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

// enterAdoptForm opens the adopt form for a discovered (unmanaged) guest.
// newAdoptBinding calls Engine.BuildAdoptSpec synchronously — one or two quick
// config reads, the same class of call as pm.Nodes() in the migrate form — so
// no loading state is needed.
func (m Model) enterAdoptForm(g proxmox.Guest) (tea.Model, tea.Cmd) {
	b, err := newAdoptBinding(m.eng, g)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.adoptB, m.form, m.formKind = b, b.form, fkAdopt
	m.formTitle = "Adopt " + g.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterSnapshotForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	// The RAM option only applies to a running VM; containers have no live-memory
	// state to capture.
	allowRAM := m.statuses[vm.Name] == "running" && !vm.IsLXC()
	b := newSnapBinding(allowRAM)
	m.snapB, m.snapVM, m.form, m.formKind = b, vm, b.form, fkSnapshot
	m.formTitle = "Snapshot " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

// enterInjectForm opens the SSH-key picker for the selected managed guest.
// newInjectBinding reads the operator's keys (config + ~/.ssh) synchronously —
// a couple of cheap local reads — so no loading state is needed.
func (m Model) enterInjectForm(vm *state.VMSpec) (tea.Model, tea.Cmd) {
	// A keyless guest (no key already trusted) can't be reached over SSH, but both
	// kinds are recoverable, so both open the picker: streamInjectKey routes a
	// keyless VM through the QEMU guest agent and a keyless LXC over the Proxmox
	// console. Only the keyless-LXC console path needs a password (below).
	//
	// A keyless LXC's first key goes in over the Proxmox console, which needs the
	// root password. If it isn't stored on this machine (created elsewhere / by an
	// older hlab), the form asks for it; otherwise the stored one is used silently.
	needPassword := false
	if len(vm.SSHKeys) == 0 && vm.IsLXC() {
		if pw, perr := m.eng.StoredCTPassword(vm.Name); perr == nil {
			needPassword = pw == ""
		}
	}
	b, err := newInjectBinding(m.eng.Cfg, vm.Name, needPassword)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.injectB, m.injectVM, m.form, m.formKind = b, vm, b.form, fkInject
	m.formTitle = "Add SSH key to " + vm.Name
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

func (m Model) enterSetupForm() (tea.Model, tea.Cmd) {
	b, err := newSetupBinding(m.eng.Cfg, m.pm)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.setupB, m.form, m.formKind = b, b.form, fkSetup
	m.formTitle = "Configure hlab"
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

// enterThemeForm opens the theme selector. newThemeBinding re-reads themes.yaml,
// so edits to the file (custom colors, new themes) appear without restarting hlab.
func (m Model) enterThemeForm() (tea.Model, tea.Cmd) {
	b := newThemeBinding(m.eng.Cfg.Theme)
	m.themeB, m.form, m.formKind = b, b.form, fkTheme
	m.formTitle = "Theme"
	m.mode, m.status, m.err = modeForm, "", nil
	return m, m.primeForm()
}

// onFormDone advances from a completed/aborted form to the next phase.
func (m Model) onFormDone() (tea.Model, tea.Cmd) {
	aborted := m.form.State == huh.StateAborted
	m.mode, m.form = modeDash, nil

	switch m.formKind {
	case fkCreate:
		if aborted || !m.createB.confirm {
			m.status = "create cancelled"
			return m, m.refresh()
		}
		res, err := m.createB.Result()
		if err != nil {
			m.err = err
			return m, m.refresh()
		}
		return m.startRun("Creating "+res.VM.Name, m.streamCreate(res))

	case fkProvision:
		if aborted {
			m.status = "provision cancelled"
			return m, m.refresh()
		}
		vm := m.provVM
		vm.Software = m.provB.software
		if len(vm.Software) == 0 {
			m.status = "nothing selected to provision"
			return m, m.refresh()
		}
		return m.startRun("Provisioning "+vm.Name, m.streamProvision(vm))

	case fkDestroy:
		if aborted || !m.destroyB.confirm {
			m.status = "destroy cancelled"
			return m, m.refresh()
		}
		return m.startRun("Destroying "+m.destroyName, m.streamDestroy(m.destroyName))

	case fkMigrate:
		if aborted || !m.migrateB.confirm {
			m.status = "migrate cancelled"
			return m, m.refresh()
		}
		vm := m.migrateVM
		return m.startRun("Migrating "+vm.Name, m.streamMigrate(vm.Name, m.migrateB.toNode))

	case fkEdit:
		if aborted || !m.editB.confirm {
			m.status = "edit cancelled"
			return m, m.refresh()
		}
		vm := m.editVM
		m.editB.apply(vm)
		return m.startRun("Reconfiguring "+vm.Name, m.streamReconfigure(vm))

	case fkSnapshot:
		if aborted || strings.TrimSpace(m.snapB.name) == "" {
			m.status = "snapshot cancelled"
			return m, m.refresh()
		}
		vm, sb := m.snapVM, m.snapB
		return m.startRun("Creating snapshot "+sb.name,
			m.streamSnapshotCreate(vm.Name, strings.TrimSpace(sb.name), sb.description, sb.withRAM))

	case fkSetup:
		if aborted {
			m.status = "setup cancelled"
			return m, nil
		}
		if err := m.setupB.Save(); err != nil {
			m.err = err
			return m, nil
		}
		m = m.reloadEngine()
		m.status = "configuration saved"
		return m, m.refresh()

	case fkAdopt:
		if aborted || !m.adoptB.confirm {
			m.status = "adopt cancelled"
			return m, m.refresh()
		}
		spec := m.adoptB.apply()
		return m.startRun("Adopting "+spec.Name, m.streamAdopt(spec))

	case fkInject:
		if aborted || !m.injectB.confirm {
			m.status = "add ssh key cancelled"
			return m, m.refresh()
		}
		vm := m.injectVM
		return m.startRun("Adding SSH key to "+vm.Name, m.streamInjectKey(vm, m.injectB.pub, m.injectB.password))

	case fkTheme:
		if aborted {
			m.status = "theme unchanged"
			return m, nil
		}
		name := m.themeB.choice
		// Apply live: initStyles re-points every package-level style var to the new
		// palette, so the next View() (dashboard, modals, gauges) renders in it — no
		// cached rendered strings exist and the tables are drawn from these vars each
		// frame. Then persist the choice so it survives a restart.
		initStyles(m.themeB.set.Get(name))
		m.eng.Cfg.Theme = name
		if err := m.eng.Cfg.Save(); err != nil {
			m.err = err
			return m, nil
		}
		m.status = "theme: " + name
		return m, nil
	}
	return m, m.refresh()
}

// primeForm themes and sizes the active form to the current window and starts
// it, so an embedded form looks like the dashboard and lays out correctly even
// before the next resize event.
func (m Model) primeForm() tea.Cmd {
	// The form lives inside the modal window (inner width ≈ box − border − padding).
	h := m.height - 12
	switch {
	case h < 6:
		h = 6
	case h > 16:
		h = 16
	}
	m.form = m.form.WithTheme(theme.Huh(active)).WithShowHelp(false).WithWidth(54).WithHeight(h)
	return m.form.Init()
}

// startRun switches to the live-run mode and kicks off a streaming operation
// alongside the progress-bar animation tick.
func (m Model) startRun(title string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.mode, m.runTitle, m.logLines, m.err = modeRun, title, nil, nil
	m.opErr = nil // starting a new operation clears the last one's error
	m.runFrame = 0
	m.logVP = viewport.New(outputW, outputH)
	m.follow = true
	m.cancelled = false
	m.syncLogViewport()
	// Bind the operation to a cancellable context so the user can abort it.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.eng.Runner.SetCtx(ctx)
	m.eng.Ctx = ctx
	return m, tea.Batch(cmd, tickCmd())
}

// syncLogViewport refreshes the scrollable output with the current (truncated)
// lines, keeping the view pinned to the newest line while following.
func (m *Model) syncLogViewport() {
	lines := make([]string, len(m.logLines))
	for i, l := range m.logLines {
		lines[i] = truncate(l, outputW)
	}
	m.logVP.SetContent(strings.Join(lines, "\n"))
	if m.follow {
		m.logVP.GotoBottom()
	}
}

type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// onRunDone returns to the dashboard after a streaming operation, optionally
// chaining into the provision form (right after a create).
func (m Model) onRunDone(msg runDoneMsg) (tea.Model, tea.Cmd) {
	// Detach the shared runner from the finished operation's context.
	m.eng.Runner.Detach()
	m.eng.Ctx = nil
	m.cancel = nil

	if m.cancelled {
		m.cancelled = false
		m.mode = modeDash
		m.status = "cancelled"
		return m, m.refresh()
	}
	if msg.err != nil {
		// Stay in modeRun on failure. The log panel holds the tool output saying WHY
		// it failed; closing the window here threw that away and left only the exit
		// status — an ansible failure reads as a bare "error: exit status 2", with
		// the actual cause (a dotfiles clone the forwarded agent couldn't
		// authenticate, say) discarded along with the panel.
		//
		// The run is over, so `opErr != nil` while in modeRun is the "finished,
		// failed, waiting to be dismissed" state: the progress bar stops animating,
		// esc closes instead of cancelling, and the output is force-shown.
		//
		// Use opErr (not err) so the follow-up refresh doesn't immediately wipe it;
		// it persists until the next operation or a manual refresh.
		m.opErr = msg.err
		m.showLog = true
		return m, m.refresh()
	}
	m.mode = modeDash
	m.status = msg.status
	if msg.thenProvision != nil {
		return m.enterProvisionForm(msg.thenProvision)
	}
	return m, m.refresh()
}

// onDriftDone returns to the dashboard after a whole-fleet drift check (key P),
// mirroring onRunDone's context teardown since it also runs via startRun/modeRun.
func (m Model) onDriftDone(msg driftDoneMsg) (tea.Model, tea.Cmd) {
	m.mode = modeDash
	// Detach the shared runner from the finished operation's context.
	m.eng.Runner.Detach()
	m.eng.Ctx = nil
	m.cancel = nil

	if m.cancelled {
		m.cancelled = false
		m.status = "cancelled"
		return m, m.refresh()
	}
	if msg.err != nil {
		m.opErr = msg.err
		return m, m.refresh()
	}

	drift := make(map[string]engine.DriftStatus, len(msg.statuses))
	drifted := 0
	for _, st := range msg.statuses {
		drift[st.Name] = st
		if st.State != "in-sync" {
			drifted++
		}
	}
	m.drift = drift
	m.driftSummary = fmt.Sprintf("%d of %d drifted", drifted, len(msg.statuses))
	m.status = m.driftSummary + " (P to re-check)"
	return m, m.refresh()
}

// reloadEngine re-reads the config and rebuilds the runner + discovery client
// after the setup form changes the connection or defaults. It returns the updated
// Model (m.pm is a plain field, so a value-receiver mutation wouldn't persist).
func (m Model) reloadEngine() Model {
	cfg, err := config.Load()
	if err != nil {
		return m
	}
	// Swap in fresh Runner/PM pointers instead of mutating the existing ones in
	// place: a refresh()/resolveGuestIPs worker may still be reading the old ones
	// on another goroutine (both capture-before-spawn), so mutating a shared field
	// would be a data race. New values leave the captured old ones immutable. The
	// workspace dir comes from the store; Out/Ctx are nil at rest (no op runs during
	// Setup). Verbose is deliberately NOT carried over: the TUI always streams via
	// Out (set per-op by the stream* actions), so run() never consults Verbose — and
	// defaulting it to 0 is strictly safer, since a stray Refresh() can't leak raw
	// terraform output to stdout and corrupt the alt-screen.
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	m.eng.Cfg = cfg
	m.eng.Runner = terraform.New(m.eng.Store.TerraformDir(), cfg)
	m.eng.PM = pm
	m.pm = pm
	return m
}

// --- view ---

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	// Center the whole dashboard as one block (column alignment preserved) so it
	// doesn't hug the left edge on wide terminals. Done before the mode switch so
	// the background stays put when a modal overlays it.
	bg := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, m.dashboardView())
	switch m.mode {
	case modeForm:
		return m.overlayModal(bg, m.formModal())
	case modeRun:
		return m.overlayModal(bg, m.runModal())
	case modeHelp:
		return m.overlayModal(bg, helpModal())
	case modeConfirm:
		return m.overlayModal(bg, confirmModal(m.confirmPrompt))
	case modeSnaps:
		return m.overlayModal(bg, m.snapsModal())
	}
	return bg
}

// snapsModal renders the snapshots browser for the selected VM: a list of its
// snapshots (name · age · RAM flag · description) with the cursor highlighted,
// and the available actions below.
func (m Model) snapsModal() string {
	lines := []string{modalTitleStyle.Render("Snapshots — " + m.snapVM.Name), ""}
	if len(m.snaps) == 0 {
		lines = append(lines, mDimStyle.Render("  no snapshots yet — press c to create one"))
	} else {
		for i, s := range m.snaps {
			marker := "  "
			nameCell := padCell(s.Name, 18)
			if i == m.snapCursor {
				marker = mLabelStyle.Render("> ")
				nameCell = mLabelStyle.Render(nameCell)
			}
			meta := padCell(humanAge(s.Time), 10)
			if s.WithRAM {
				meta += "RAM "
			} else {
				meta += "    "
			}
			if s.Description != "" {
				meta += s.Description
			}
			lines = append(lines, marker+nameCell+" "+mDimStyle.Render(meta))
		}
	}
	lines = append(lines, "",
		mDimStyle.Render("c create · r rollback · d delete · ↑/↓ move · esc close"))
	return modalStyle.Render(strings.Join(lines, "\n"))
}

// humanAge formats a unix timestamp as a short relative age ("3h ago", "2d ago").
func humanAge(unix int64) string {
	if unix <= 0 {
		return "—"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

// confirmModal renders a centered yes/no window for a discovered power action.
func confirmModal(prompt string) string {
	lines := []string{
		modalTitleStyle.Render("Confirm"), "",
		mDimStyle.Render(prompt), "",
		mLabelStyle.Render(" y ") + mDimStyle.Render(" yes    ") + mLabelStyle.Render(" n ") + mDimStyle.Render(" no"),
	}
	return modalStyle.Render(strings.Join(lines, "\n"))
}

// helpModal lists every keybinding in a centered window.
func helpModal() string {
	row := func(k, d string) string {
		return mLabelStyle.Render(fmt.Sprintf(" %-7s", k)) + mDimStyle.Render(d)
	}
	lines := []string{
		modalTitleStyle.Render("Keybindings"), "",
		// Navigation + create.
		row("↑/↓", "move selection (flows across both sections)"),
		row("n", "new VM (create + provision)"),
		"",
		// Actions on the selected guest: provision, access/power, config, adopt.
		row("p", "provision selected VM"),
		row("u", "update (re-provision) selected VM"),
		row("U", "update + upgrade packages/runtimes"),
		row("s", "ssh into selected VM"),
		row("i", "inject an SSH key into selected VM"),
		row("b", "start / stop selected VM or guest"),
		row("r", "reboot selected VM or guest"),
		row("e", "edit CPU / RAM / disk of selected VM"),
		row("m", "migrate selected VM to another node"),
		row("v", "snapshots (create/rollback/delete)"),
		row("d", "destroy selected VM"),
		row("a", "adopt selected discovered guest"),
		"",
		// Fleet / app-wide.
		row("P", "plan / detect drift (whole fleet)"),
		row("R", "refresh now"),
		row("S", "setup / configuration"),
		row("t", "theme (change color theme)"),
		row("l", "show/hide output (during a run)"),
		row("q", "quit"),
		row("?", "keybindings (this window)"),
		"", mDimStyle.Render("discovered guests (not managed): power (b/r,"),
		mDimStyle.Render("with a yes/no confirmation) and a to adopt."),
		"", mDimStyle.Render("any key closes this"),
	}
	return modalStyle.Render(strings.Join(lines, "\n"))
}

// overlayModal composites a modal box centered over the dashboard background.
func (m Model) overlayModal(bg, box string) string {
	bg = padToHeight(bg, m.height)
	bw, bh := lipgloss.Size(box)
	return overlay(bg, box, max((m.width-bw)/2, 0), max((m.height-bh)/2, 0))
}

// dashboardView renders the always-present background. The detail panel and the
// footer are pinned to the bottom of the screen: the two tables fill the space
// above them and a flexible gap absorbs the difference, so the panel and the
// keybinding line don't move as you navigate (the Discovered window changing
// height no longer shifts them). The tables are windowed to whatever height is
// left — Managed stays visible at the top while a long Discovered list scrolls
// around the cursor.
func (m Model) dashboardView() string {
	// Bottom row: the per-guest detail panel, and — when the terminal is wide
	// enough for the full tables — the global metrics panel joined to its right.
	// detail(48) + gap(1) + metrics(40) == tableWidth(managedCols) (89), so the
	// joined row is exactly as wide as the tables and doesn't widen the centered
	// block (keeps the version alignment; see TestVersionAlignsToTableNotFooter).
	// On a narrower terminal the metrics panel is dropped so nothing wraps.
	detail := m.detailView()
	if m.width >= tableWidth(managedCols)+4 {
		detail = lipgloss.JoinHorizontal(lipgloss.Top, detail, " ", m.metricsView())
	}
	bottom := detail + "\n" + m.footerView()
	bottomH := lipgloss.Height(bottom)

	// Leave one empty row at the very bottom: some terminals (and overlays like
	// Warp's notifications) clip the last line, which would hide the footer.
	const bottomMargin = 1
	// Fixed top chrome: title (2) + managed header+rule (2) + gap (1) +
	// discovered title+header+rule (3) + up to four ↑/↓ scroll hints (4).
	const topChrome = 12
	budget := m.height - bottomH - topChrome - bottomMargin
	budget = max(budget, 4)
	mShow, dShow := m.splitBudget(budget)

	// Build the tables first. The version in the title bar is right-aligned to the
	// TABLE width (body) only — NOT to max(body, bottom): the footer keybinding line
	// (part of bottom) can be wider than the tables, and its width depends on the
	// selection (a managed VM's footer is longer than a discovered guest's), so
	// aligning the version to it would push the version past the tables' right edge
	// when a managed guest is selected. View() still centers the whole block by its
	// true widest line, so the footer isn't clipped.
	body := m.managedSection(mShow)
	if d := m.discoveredSection(dShow); d != "" {
		body += "\n\n" + d
	}
	contentW := lipgloss.Width(body)

	// Landing-page match: accent prompt char, bright title text.
	title := accentStyle.Render("❯") + " " + titleStyle.Render("hlab — homelab")
	if m.version != "" {
		version := dimStyle.Render(m.version)
		if pad := contentW - lipgloss.Width(title) - lipgloss.Width(version); pad > 2 {
			title += strings.Repeat(" ", pad) + version
		} else {
			title += "  " + version
		}
	}
	topStr := title + "\n\n" + body

	gap := m.height - lipgloss.Height(topStr) - bottomH - bottomMargin
	gap = max(gap, 0)
	return topStr + strings.Repeat("\n", gap) + bottom
}

// splitBudget divides the row budget between the Managed and Discovered tables,
// keeping Managed visible (it never takes more than half when both are present)
// and giving the rest to Discovered.
func (m Model) splitBudget(budget int) (mShow, dShow int) {
	mTotal, dTotal := len(m.vms), len(m.guests)
	switch {
	case dTotal == 0:
		return budget, 0
	case mTotal == 0:
		return 0, budget
	default:
		mShow = mTotal
		if half := budget / 2; mShow > half {
			mShow = half
		}
		mShow = max(mShow, 1)
		return mShow, budget - mShow
	}
}

// windowRange returns the [start,end) slice of n rows to show in a viewport of
// maxRows, keeping the selected row sel visible, plus whether rows are hidden
// above (less) or below (more).
func windowRange(n, maxRows, sel int) (start, end int, less, more bool) {
	if maxRows >= n {
		return 0, n, false, false
	}
	start = sel - maxRows/2
	start = max(start, 0)
	if start+maxRows > n {
		start = n - maxRows
	}
	return start, start + maxRows, start > 0, start+maxRows < n
}

// managedSection renders the table of hlab-managed VMs, windowed to maxRows.
func (m Model) managedSection(maxRows int) string {
	var b strings.Builder
	b.WriteString(headerLine(managedCols))
	b.WriteByte('\n')
	b.WriteString(ruleLine(managedCols))
	b.WriteByte('\n')
	if len(m.vms) == 0 {
		b.WriteString(dimStyle.Render("  no managed VMs — press n to create one"))
		return b.String()
	}
	sel := 0
	if m.cursor < len(m.vms) {
		sel = m.cursor
	}
	start, end, less, more := windowRange(len(m.vms), maxRows, sel)
	if less {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		vm := m.vms[i]
		running := m.statuses[vm.Name] == "running"
		selected := i == m.cursor
		var bg lipgloss.Color
		if selected {
			bg = selBG
		}
		cpuFrac := 0.0
		if running {
			cpuFrac = m.live[vm.Name].CPUFrac
		}
		cells := []cell{
			gutterCell(selected),
			dotCell(running),
			nameCell(vm.Name, running, hasDrift(m.drift[vm.Name]), bg),
			{text: kindLabel(vm.Kind()), style: dimStyle},
			{text: fmt.Sprintf("%d", vm.VMID), style: faintStyle},
			{text: vm.Node, style: dimStyle},
			{pre: meterBarBG(cpuFrac, 6, bg)},
			{text: memTag(declaredMemMB(vm)), style: dimStyle},
			{text: managedIP(vm, m.ips), style: dimStyle},
			{text: provisioned(vm), style: dimStyle},
		}
		b.WriteString(rowLine(cells, managedCols, bg))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if more {
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.vms)-end)))
	}
	return b.String()
}

// discoveredSection renders the table of guests (VMs/LXC) that exist in Proxmox
// but are not managed by hlab, windowed to maxRows. Returns "" when there are
// none.
func (m Model) discoveredSection(maxRows int) string {
	if len(m.guests) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(sectionTitleStyle.Render("discovered"))
	b.WriteString(dimStyle.Render("  not managed by hlab — power only"))
	b.WriteByte('\n')
	b.WriteString(headerLine(discoveredCols))
	b.WriteByte('\n')
	b.WriteString(ruleLine(discoveredCols))
	b.WriteByte('\n')

	sel := 0
	if i := m.cursor - len(m.vms); i >= 0 {
		sel = i
	}
	start, end, less, more := windowRange(len(m.guests), maxRows, sel)
	if less {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		g := m.guests[i]
		running := g.Status == "running"
		selected := len(m.vms)+i == m.cursor
		var bg lipgloss.Color
		if selected {
			bg = selBG
		}
		// Discovered rows use the SAME per-cell styling as managed so both tables
		// read identically; the "not managed" distinction stays in the section
		// title and the empty provisioned column. Discovered guests never show a
		// drift marker (hlab doesn't track their desired state).
		cells := []cell{
			gutterCell(selected),
			dotCell(running),
			nameCell(g.Name, running, false, bg),
			{text: kindLabel(g.Type), style: dimStyle},
			{text: fmt.Sprintf("%d", g.VMID), style: faintStyle},
			{text: g.Node, style: dimStyle},
			{pre: meterBarBG(g.CPUFrac, 6, bg)},
			{text: memTag(g.MemMB), style: dimStyle},
			{text: ipOrDashCell(g.IP), style: dimStyle},
			{text: "", style: dimStyle},
		}
		b.WriteString(rowLine(cells, discoveredCols, bg))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if more {
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.guests)-end)))
	}
	return b.String()
}

// formModal renders the active wizard as a centered, bordered window that floats
// over the dashboard.
func (m Model) formModal() string {
	inner := modalTitleStyle.Render(m.formTitle) + "\n\n" +
		m.form.View() + "\n" +
		mDimStyle.Render("tab/enter: next · shift+tab: back · esc: cancel")
	return modalStyle.Render(inner)
}

// runModal renders the running operation as a fixed-size centered window: an
// animated progress bar, the latest output line, and (toggled with l) a
// fixed-size output box that does not grow as the process advances.
func (m Model) runModal() string {
	const inner = 56         // modal content width
	failed := m.opErr != nil // the run finished and failed; held open to be read
	var b strings.Builder
	if failed {
		b.WriteString(modalTitleStyle.Render(strings.TrimSuffix(m.runTitle, "…") + " — failed"))
		b.WriteString("\n\n")
		// The exit status alone is useless, so lead with it but keep the output box
		// below: that is where the real cause is.
		b.WriteString(mErrStyle.Render(truncate(m.opErr.Error(), inner)))
		b.WriteString("\n\n")
	} else {
		b.WriteString(modalTitleStyle.Render(m.runTitle + "…"))
		b.WriteString("\n\n")
		b.WriteString(progressBar(inner, m.runFrame))
		b.WriteString("\n\n")
	}

	if m.showLog {
		hint := "↑/↓ scroll"
		if m.follow {
			hint = "following"
		}
		b.WriteString(m.outputBox())
		b.WriteByte('\n')
		b.WriteString(mDimStyle.Render(hint))
		b.WriteByte('\n')
	} else {
		last := ""
		if n := len(m.logLines); n > 0 {
			last = m.logLines[n-1]
		}
		b.WriteString(mDimStyle.Render(truncate(last, inner)))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	action := "esc: cancel"
	if failed {
		action = "esc: close" // nothing left to cancel — the run already ended
	}
	b.WriteString(mDimStyle.Render("l: " + showHideLabel(m.showLog) + " output · " + action))
	return modalStyle.Render(b.String())
}

// outputBox renders the scrollable, fixed-size output viewport as a dark box.
func (m Model) outputBox() string {
	return outputBoxStyle.Render(m.logVP.View())
}

func showHideLabel(shown bool) string {
	if shown {
		return "hide"
	}
	return "show"
}

// progressBar renders an indeterminate marquee bar of the given width.
func progressBar(width, frame int) string {
	width = max(width, 6)
	seg := max(width/4, 3)
	span := width + seg
	start := frame%span - seg
	onStart := clamp(start, 0, width)
	onEnd := clamp(start+seg, 0, width)
	return barOffStyle.Render(strings.Repeat("─", onStart)) +
		barOnStyle.Render(strings.Repeat("━", onEnd-onStart)) +
		barOffStyle.Render(strings.Repeat("─", width-onEnd))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m Model) detailView() string {
	if vm := m.selectedVM(); vm != nil {
		g, hasLive := m.live[vm.Name]
		running := hasLive && g.Status == "running"
		status := m.statuses[vm.Name]
		if status == "" {
			status = "unknown"
		}
		if running && g.Uptime > 0 {
			status += dimStyle.Render(" · up " + humanUptime(g.Uptime))
		}
		net := "DHCP"
		if !vm.DHCP {
			net = vm.IPCIDR + " gw " + vm.Gateway
		}
		spec := fmt.Sprintf("%d cores · %s RAM · %d GB disk", vm.Cores, memShort(vm), vm.DiskGB)
		if vm.Plan != "" {
			spec = vm.Plan + " · " + spec
		}
		// Live RAM. The balloon figure Proxmox reports (g.MemUsedMB) counts the
		// guest's reclaimable page cache as "used", so it reads near-full on a healthy
		// Linux guest. Prefer the guest's own accounting from the QEMU agent
		// (m.realMem: MemTotal-MemAvailable), which matches `free`/btop; fall back to
		// the balloon figure when no agent reading is available (LXC, or agent down).
		usedMB, maxMB := g.MemUsedMB, declaredMemMB(vm)
		if hasLive && g.MemMB > 0 {
			maxMB = g.MemMB
		}
		if rm, ok := m.realMem[vm.Name]; ok && rm.TotalMB > 0 {
			usedMB, maxMB = rm.UsedMB, rm.TotalMB
		}
		lines := []string{
			faintStyle.Render("name  ") + headingStyle.Render(vm.Name),
			faintStyle.Render("state ") + statusDot(running) + " " + status,
			faintStyle.Render("cpu   ") + cpuGauge(g.CPUFrac, running),
			faintStyle.Render("ram   ") + ramGauge(usedMB, maxMB, running),
			faintStyle.Render("spec  ") + spec,
			faintStyle.Render("model ") + cpuModelCell(vm),
			faintStyle.Render("net   ") + net,
			faintStyle.Render("user  ") + vm.Username,
			faintStyle.Render("prov  ") + provisioned(vm),
			faintStyle.Render("drift ") + driftLine(m.drift, vm.Name),
		}
		return panelStyle.Render(strings.Join(lines, "\n"))
	}
	if g := m.selectedGuest(); g != nil {
		running := g.Status == "running"
		lines := []string{
			faintStyle.Render("name  ") + headingStyle.Render(g.Name),
			faintStyle.Render("state ") + statusDot(running) + " " + statusOr(g.Status),
			faintStyle.Render("cpu   ") + cpuGauge(g.CPUFrac, running),
			faintStyle.Render("ram   ") + ramGauge(g.MemUsedMB, g.MemMB, running),
			faintStyle.Render("type  ") + kindLabel(g.Type) + dimStyle.Render("  (not managed by hlab)"),
			faintStyle.Render("node  ") + g.Node,
			faintStyle.Render("ip    ") + ipOrDashCell(g.IP),
		}
		return panelStyle.Render(strings.Join(lines, "\n"))
	}
	return panelStyle.Render(dimStyle.Render("no selection"))
}

// metricsInnerW is the metrics panel's inner content width. With padding 0,1 and a
// 1-cell border that is 38+2+2 = 40 columns outer; see metricsView/dashboardView.
// metricsBodyW is the column the free-capacity values (and their "free" header)
// right-align to, leaving a small right margin inside the 36-col text area.
const (
	metricsInnerW = 38
	metricsBodyW  = 34
	metricsBarW   = 16
)

// metricsView renders the global fleet/cluster metrics panel shown to the right of
// the per-guest detail panel: a compact fleet header (total guests · running),
// then per host node CPU/RAM bars and that node's local storage usage. It is
// GLOBAL — independent of the selection. Node/storage data comes from m.metrics
// (proxmox.ClusterMetrics); the fleet counts are derived from the already-loaded
// model, so they need no extra call.
func (m Model) metricsView() string {
	if len(m.metrics.Nodes) == 0 {
		return metricsPanelStyle.Render(dimStyle.Render("cluster metrics loading…"))
	}

	total, running := m.fleetCounts()
	// "cluster" header — accent (non-bold) label + faint summary, matching the
	// landing mock's lowercase section language.
	header := accentStyle.Render("cluster") +
		faintStyle.Render(fmt.Sprintf(" · %d guests · %d up", total, running))
	// "free" is a column title right-aligned over the per-node free-capacity values
	// (so the word isn't repeated on every ram/disk line).
	free := faintStyle.Render("free")
	if pad := metricsBodyW - lipgloss.Width(header) - lipgloss.Width(free); pad >= 1 {
		header += strings.Repeat(" ", pad) + free
	}
	lines := []string{header}

	// One stacked block per node: name, then cpu/ram/disk meters. The count
	// covers the whole node — m.guests holds only discovered guests, so the
	// managed ones (m.live) are appended before tallying.
	stByNode := m.storageByNode()
	all := make([]proxmox.Guest, 0, len(m.guests)+len(m.live))
	all = append(all, m.guests...)
	for _, g := range m.live {
		all = append(all, g)
	}
	counts := nodeGuestCounts(all)
	var body []string
	for _, n := range m.metrics.Nodes {
		body = append(body, nodeMetricBlock(n, primaryStorage(stByNode[n.Name]), counts[n.Name])...)
	}
	// Fit the body to the panel height (9 rows: 1 header + 8 body).
	const bodyMax = 8
	if len(body) > bodyMax {
		hidden := len(body) - (bodyMax - 1)
		body = append(body[:bodyMax-1], dimStyle.Render(fmt.Sprintf("… +%d", hidden)))
	}
	lines = append(lines, body...)
	return metricsPanelStyle.Render(strings.Join(lines, "\n"))
}

// fleetCounts returns the total number of real guests (managed + discovered,
// excluding templates) and how many are running — the metrics panel header.
func (m Model) fleetCounts() (total, running int) {
	total = len(m.vms)
	for _, vm := range m.vms {
		if m.statuses[vm.Name] == "running" {
			running++
		}
	}
	for _, g := range m.guests {
		if g.Template {
			continue
		}
		total++
		if g.Status == "running" {
			running++
		}
	}
	return total, running
}

// nodeMetricBlock renders one host node as a stacked block: the node name line
// (accent name, " online" when online, and a right-aligned "K/N up" guest count),
// then a cpu/ram/disk meter line each. An offline node shows its status and no
// meters. disk is the node's primary guest-backing storage (may be nil); count is
// {running, total} guests on this node (from nodeGuestCounts).
func nodeMetricBlock(n proxmox.NodeMetric, disk *proxmox.StorageMetric, count [2]int) []string {
	name := accentStyle.Render(n.Name)
	if n.Status != "" && n.Status != "online" {
		return []string{name + " " + dimStyle.Render(n.Status)}
	}
	nameLine := name
	if n.Status == "online" {
		nameLine += faintStyle.Render(" online")
	}
	// Right-align "K/N up" for nodes that carry any guests, filling to metricsBodyW.
	if count[1] > 0 {
		up := faintStyle.Render(fmt.Sprintf("%d/%d up", count[0], count[1]))
		if pad := metricsBodyW - lipgloss.Width(nameLine) - lipgloss.Width(up); pad >= 1 {
			nameLine += strings.Repeat(" ", pad) + up
		}
	}
	out := []string{nameLine, meterLine("cpu", n.CPUFrac, 0, false)}
	if n.MemMaxMB > 0 {
		memFrac := float64(n.MemUsedMB) / float64(n.MemMaxMB)
		freeGB := int((n.MemMaxMB - n.MemUsedMB + 512) / 1024) // round MB→GB
		out = append(out, meterLine("ram", memFrac, freeGB, true))
	}
	if disk != nil && disk.TotalGB > 0 {
		dFrac := float64(disk.UsedGB) / float64(disk.TotalGB)
		out = append(out, meterLine("dsk", dFrac, int(disk.TotalGB-disk.UsedGB), true))
	}
	return out
}

// nodeGuestCounts tallies guests per host node (discovered + managed combined
// by the caller), returning node name → {running, total}. Templates are
// excluded (they are not real guests). Pure — unit-tested.
func nodeGuestCounts(guests []proxmox.Guest) map[string][2]int {
	out := map[string][2]int{}
	for _, g := range guests {
		if g.Template {
			continue
		}
		c := out[g.Node]
		c[1]++
		if g.Status == "running" {
			c[0]++
		}
		out[g.Node] = c
	}
	return out
}

// meterLine renders one indented metric row: "  ram  ⣿⣿⣿⣿⡀⡀⡀⡀  69%      5G", where
// the free capacity (shown only when showFree is set) is right-aligned to the
// panel's free column, under the "free" header — so the word isn't repeated.
func meterLine(label string, frac float64, freeGB int, showFree bool) string {
	s := fmt.Sprintf("  %-5s", label) + meterBar(frac, metricsBarW) + fmt.Sprintf(" %3d%%", pct(frac))
	if showFree {
		free := humanGB(freeGB)
		if pad := metricsBodyW - lipgloss.Width(s) - len(free); pad >= 1 {
			s += strings.Repeat(" ", pad) + dimStyle.Render(free)
		}
	}
	return s
}

// humanGB formats a GiB count compactly: "512G", switching to "1.8T" (one decimal)
// once it reaches 1000 GB so large storages don't render as four-digit gigabytes.
func humanGB(gb int) string {
	if gb >= 1000 {
		return fmt.Sprintf("%.1fT", float64(gb)/1024)
	}
	return fmt.Sprintf("%dG", gb)
}

// storageByNode groups the relevant (guest-backing) storages under their node,
// deduping a shared storage so it's counted once (under the first node seen).
func (m Model) storageByNode() map[string][]proxmox.StorageMetric {
	out := map[string][]proxmox.StorageMetric{}
	seenShared := map[string]bool{}
	for _, s := range m.metrics.Storage {
		if !storageRelevant(s) || (s.Shared && seenShared[s.Name]) {
			continue
		}
		if s.Shared {
			seenShared[s.Name] = true
		}
		out[s.Node] = append(out[s.Node], s)
	}
	return out
}

// primaryStorage returns the largest (by total capacity) storage in the list, or
// nil when there is none — the one shown as a node's "disk" meter.
func primaryStorage(list []proxmox.StorageMetric) *proxmox.StorageMetric {
	var best *proxmox.StorageMetric
	for i := range list {
		if best == nil || list[i].TotalGB > best.TotalGB {
			best = &list[i]
		}
	}
	return best
}

// storageRelevant keeps only real, guest-backing storages (disk images / container
// rootfs). /cluster/resources may omit the content field, in which case any
// non-empty storage is shown.
func storageRelevant(s proxmox.StorageMetric) bool {
	if s.TotalGB <= 0 || (s.Status != "" && s.Status != "available") {
		return false
	}
	if s.Content == "" {
		return true
	}
	return strings.Contains(s.Content, "images") || strings.Contains(s.Content, "rootdir")
}

// pct converts a 0..1 fraction to a clamped rounded percentage.
func pct(frac float64) int {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return int(frac*100 + 0.5)
}

// driftLine renders the detail panel's "Drift" line for a managed VM: a hint to
// run the check if it hasn't been (or the entry was pruned), "in sync" for a
// clean guest, or the drift state + diverging attributes (truncated to fit the
// panel) otherwise.
// statusDot returns the same power indicator the tables use — a filled ● (Good)
// for a running guest, a hollow ○ (Faint) otherwise — for the detail State line.
func statusDot(running bool) string {
	if running {
		return okStyle.Render("●")
	}
	return faintStyle.Render("○")
}

func driftLine(drift map[string]engine.DriftStatus, name string) string {
	d, ok := drift[name]
	if !ok {
		return dimStyle.Render("press P to check")
	}
	if d.State == "in-sync" {
		return okStyle.Render("in sync")
	}
	detail := d.State
	if len(d.Attrs) > 0 {
		detail += ": " + strings.Join(d.Attrs, ", ")
	}
	return errStyle.Render(truncate(detail, 38))
}

// declaredMemMB returns a guest's declared memory in MB (LXC stores MB, VMs GB).
func declaredMemMB(vm *state.VMSpec) int {
	if vm.MemoryMB > 0 {
		return vm.MemoryMB
	}
	return vm.MemoryGB * 1024
}

// memShort renders a guest's declared memory compactly: "512MB" for containers
// (sub-GB) and "2GB" for VMs.
func memShort(vm *state.VMSpec) string {
	mb := declaredMemMB(vm)
	if mb%1024 != 0 {
		return fmt.Sprintf("%dMB", mb)
	}
	return fmt.Sprintf("%dGB", mb/1024)
}

// cpuGauge renders a compact bar + percentage for a CPU utilization fraction
// (0..1, where 1 = all allocated cores fully used). Stopped guests show a dash.
func cpuGauge(frac float64, running bool) string {
	if !running {
		return dimStyle.Render("—")
	}
	return meterBar(frac, 10) + fmt.Sprintf(" %d%%", pct(frac))
}

// ramGauge renders a bar + used/total for memory. Stopped guests (or unknown
// sizing) show a dash.
func ramGauge(usedMB, maxMB int, running bool) string {
	if !running || maxMB <= 0 {
		return dimStyle.Render("—")
	}
	frac := float64(usedMB) / float64(maxMB)
	return meterBar(frac, 10) + fmt.Sprintf(" %.1f / %.1f GB", float64(usedMB)/1024, float64(maxMB)/1024)
}

// gaugeColor maps a 0..1 fraction to the meter fill color: green, then yellow at
// 70%, then red at 90%.
func gaugeColor(frac float64) lipgloss.Color {
	switch {
	case frac >= 0.9:
		return active.Bad // red
	case frac >= 0.7:
		return active.Warn // yellow
	default:
		return active.Good // green
	}
}

// meterBar renders a compact braille bar for a 0..1 fraction: `cells` cells, the
// filled ones ⣿ (colored by level) and the rest ⡀ as a dim track. No brackets, so
// it reads as one clean run. Whole-cell quantized (13 levels at width 12).
func meterBar(frac float64, cells int) string {
	return meterBarBG(frac, cells, "")
}

// meterBarBG renders the braille meter with an optional background baked
// into both fill and track styles. lipgloss does not re-fill a background
// behind nested styled spans (see the barOnStyle note), so composite cells
// rendered onto a highlighted row must carry the bg in every span.
func meterBarBG(frac float64, cells int, bg lipgloss.Color) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	on := int(frac*float64(cells) + 0.5)
	fillStyle := lipgloss.NewStyle().Foreground(gaugeColor(frac))
	trackStyle := lineStyle
	if bg != "" {
		fillStyle = fillStyle.Background(bg)
		trackStyle = trackStyle.Background(bg)
	}
	var b strings.Builder
	if on > 0 {
		b.WriteString(fillStyle.Render(strings.Repeat("⣿", on)))
	}
	if cells-on > 0 {
		b.WriteString(trackStyle.Render(strings.Repeat("⡀", cells-on)))
	}
	return b.String()
}

// humanUptime formats a boot age in seconds as a short "3d4h" / "5h12m" / "8m".
func humanUptime(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// footerView always renders two lines — a status/error line (blank when there is
// none) above the keybinding line — so the footer keeps a constant height and the
// keybindings never shift or disappear when a status message appears.
func (m Model) footerView() string {
	if m.mode == modeRun {
		if m.opErr != nil {
			return dimStyle.Render("  failed — read the output above, esc to close") + "\n"
		}
		return dimStyle.Render("  working… please wait") + "\n"
	}
	// Context-sensitive: a discovered guest only exposes power actions.
	hints := []keyHint{
		{"↑/↓", "move"}, {"n", "new"}, {"p", "provision"}, {"s", "ssh"},
		{"b", "power"}, {"r", "reboot"}, {"?", "keybindings"}, {"q", "quit"},
	}
	if m.selectedGuest() != nil {
		hints = []keyHint{
			{"↑/↓", "move"}, {"a", "adopt"}, {"b", "start/stop"}, {"r", "reboot"},
			{"n", "new"}, {"?", "keybindings"}, {"q", "quit"},
		}
	}
	var status string
	switch {
	case m.opErr != nil:
		status = errStyle.Render("  error: " + m.opErr.Error())
	case m.err != nil:
		status = errStyle.Render("  error: " + m.err.Error())
	case m.status != "":
		status = okStyle.Render("  " + m.status)
	}
	return status + "\n" + renderHints(hints)
}

// keyHint is one footer keybinding: a key and what it does. Rendered two-tone —
// the key in dimStyle (slightly brighter), the description in faintStyle — to
// match the landing mock's legend.
type keyHint struct{ key, desc string }

// renderHints renders a keyHint slice as "  ↑/↓ move · n new · …", the key one
// step brighter than its description and the " · " separators faint.
func renderHints(hints []keyHint) string {
	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = dimStyle.Render(h.key) + " " + faintStyle.Render(h.desc)
	}
	return "  " + strings.Join(parts, faintStyle.Render(" · "))
}

// selectedVM returns the managed VM under the cursor, or nil if the cursor is on
// a discovered guest (or there are none).
func (m Model) selectedVM() *state.VMSpec {
	if m.cursor >= 0 && m.cursor < len(m.vms) {
		return m.vms[m.cursor]
	}
	return nil
}

// selectedGuest returns the discovered guest under the cursor, or nil if the
// cursor is on a managed VM.
func (m Model) selectedGuest() *proxmox.Guest {
	i := m.cursor - len(m.vms)
	if i >= 0 && i < len(m.guests) {
		return &m.guests[i]
	}
	return nil
}

func asForm(model tea.Model, fallback *huh.Form) *huh.Form {
	if f, ok := model.(*huh.Form); ok {
		return f
	}
	return fallback
}

// --- table data ---

// colSpec is one fixed-width column in a manually-rendered section table.
type colSpec struct {
	title string
	w     int
}

var (
	// The dashboard table columns (sum == 89, the tableWidth invariant the layout
	// math relies on). Two leading title-less columns render the selection gutter
	// (an accent "▎") and a power dot (● running / ○ stopped); the STATUS text
	// column was dropped — power now reads from the dot + name brightness, and drift
	// from a "!" suffix on the name.
	managedCols = []colSpec{
		{"", 1}, {"", 2}, {"name", 16}, {"kind", 5}, {"id", 6}, {"node", 9},
		{"cpu", 9}, {"mem", 7}, {"ip", 15}, {"provisioned", 19},
	}
	// Discovered uses the SAME columns as managed so the two tables line up under a
	// centered layout. IP is resolved best-effort (agent / LXC namespace); the
	// provisioned column is shown for alignment but stays blank (hlab does not
	// provision unmanaged guests).
	discoveredCols = []colSpec{
		{"", 1}, {"", 2}, {"name", 16}, {"kind", 5}, {"id", 6}, {"node", 9},
		{"cpu", 9}, {"mem", 7}, {"ip", 15}, {"provisioned", 19},
	}
)

// tableWidth is the total character width of a column set.
func tableWidth(cols []colSpec) int {
	w := 0
	for _, c := range cols {
		w += c.w
	}
	return w
}

// headerLine renders the blue column-header row, padded to the full table width
// so every line shares the same width (required for the centered layout to keep
// columns aligned).
func headerLine(cols []colSpec) string {
	var b strings.Builder
	for _, c := range cols {
		b.WriteString(padCell(c.title, c.w))
	}
	return tableHeaderStyle.Render(b.String())
}

// ruleLine renders the separator under the column headers.
func ruleLine(cols []colSpec) string {
	return tableRuleStyle.Render(strings.Repeat("─", tableWidth(cols)))
}

// cell is one table cell: plain text plus a foreground style, or (for
// composite content like a braille meter) a pre-styled ANSI string in pre.
type cell struct {
	text  string
	style lipgloss.Style
	pre   string // when non-empty, already-styled content; bg must be baked in by the caller
}

// rowLine renders one table row: each cell padded to its column width, with
// bg (zero value = none) composed into every span — cell text AND padding —
// so a selected row reads as a full-width highlight while each cell keeps
// its own foreground. Columns are joined with no separator (each width already
// includes its trailing spacing) so the total row width stays tableWidth(cols).
func rowLine(cells []cell, cols []colSpec, bg lipgloss.Color) string {
	var b strings.Builder
	for i, col := range cols {
		var c cell
		if i < len(cells) {
			c = cells[i]
		}
		if c.pre != "" {
			// Pre-styled content: measure/truncate ANSI-aware, then pad with
			// bg-carrying spaces (the caller already baked bg into c.pre).
			content := c.pre
			if ansi.StringWidth(content) > col.w {
				content = ansi.Truncate(content, col.w, "…")
			}
			b.WriteString(content)
			if padN := col.w - ansi.StringWidth(content); padN > 0 {
				if bg != "" {
					b.WriteString(lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", padN)))
				} else {
					b.WriteString(strings.Repeat(" ", padN))
				}
			}
			continue
		}
		// Simple cell: pad the PLAIN text first (so truncation/pad math never
		// sees ANSI), then render with the cell's fg plus the row bg. The pad
		// spaces are inside Render, so they carry the bg too.
		padded := padCell(c.text, col.w)
		st := c.style
		if bg != "" {
			st = st.Background(bg)
		}
		b.WriteString(st.Render(padded))
	}
	return b.String()
}

// padCell truncates (with an ellipsis) or right-pads s to exactly w columns. A
// truncated value keeps a trailing space so the ellipsis never abuts the next
// column (which made long names run into the ID column). Measured in display
// cells (ansi.StringWidth/Truncate), not rune count, so a wide/CJK rune counts
// as the two columns it actually occupies and columns stay aligned.
func padCell(s string, w int) string {
	if ansi.StringWidth(s) > w {
		switch {
		case w > 2:
			s = ansi.Truncate(s, w-1, "…") + " "
		case w > 1:
			s = ansi.Truncate(s, w, "…")
		default:
			s = ansi.Truncate(s, w, "")
		}
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// managedIP returns the best-known IP for a managed VM (declared static, else
// the guest-agent address), or "—".
func managedIP(vm *state.VMSpec, ips map[string][]string) string {
	ip := engine.DeclaredIP(vm)
	if ip == "" {
		ip = engine.FirstIPv4(ips[vm.Name])
	}
	if ip == "" {
		return "—"
	}
	return ip
}

// kindLabel maps a Proxmox guest kind to a user-facing label: "qemu" → "vm",
// "lxc" → "lxc".
func kindLabel(kind string) string {
	if kind == "lxc" {
		return "lxc"
	}
	return "vm"
}

// ipOrDashCell returns the IP, or "—" when unknown (stopped / no agent).
func ipOrDashCell(ip string) string {
	if ip == "" {
		return "—"
	}
	return ip
}

// statusOr returns s, or "—" when empty.
func statusOr(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// hasDrift reports whether the last drift check (key P) found real drift for a
// guest — the signal for the NAME cell's "!" marker.
func hasDrift(d engine.DriftStatus) bool {
	return d.State != "" && d.State != "in-sync"
}

// gutterCell renders the 1-wide leading selection gutter: an accent "▎" bar on
// the selected row, a blank otherwise.
func gutterCell(selected bool) cell {
	if selected {
		return cell{text: "▎", style: accentStyle}
	}
	return cell{text: " "}
}

// dotCell renders the 2-wide power indicator: a filled "●" (Good) for a running
// guest, a hollow "○" (Faint) for a stopped/unknown one.
func dotCell(running bool) cell {
	if running {
		return cell{text: "●", style: okStyle}
	}
	return cell{text: "○", style: faintStyle}
}

// nameCell builds the NAME cell (column width 16): bright (heading) for a running
// guest, muted (dim) otherwise. When the guest has drifted it becomes a pre-styled
// cell — the (truncated) name followed by a Warn "!" — with the selected-row bg
// baked into every span so the highlight is unbroken.
func nameCell(name string, running, drifted bool, bg lipgloss.Color) cell {
	nameStyle := dimStyle
	if running {
		nameStyle = headingStyle
	}
	if !drifted {
		return cell{text: name, style: nameStyle}
	}
	ns, ws := nameStyle, warnStyle
	if bg != "" {
		ns, ws = ns.Background(bg), ws.Background(bg)
	}
	// Reserve " !" (2 cols) inside the 16-wide name column; rowLine pads the rest.
	trunc := name
	if ansi.StringWidth(trunc) > 14 {
		trunc = ansi.Truncate(trunc, 13, "…") + " "
	}
	return cell{pre: ns.Render(trunc+" ") + ws.Render("!")}
}

// memTag renders a declared memory size compactly with a single-letter suffix:
// whole gigabytes as "4G", sub-GB sizes as "512M". Zero/unknown → "—".
func memTag(mb int) string {
	switch {
	case mb <= 0:
		return "—"
	case mb%1024 == 0:
		return fmt.Sprintf("%dG", mb/1024)
	default:
		return fmt.Sprintf("%dM", mb)
	}
}

func provisioned(vm *state.VMSpec) string {
	if len(vm.Software) == 0 {
		return "—"
	}
	return strings.Join(vm.Software, ",")
}

// --- styles ---

// active is the semantic palette currently driving the styles below. It is set to
// the built-in default at package load (so tests and any path that doesn't call
// initStyles keep today's look) and re-pointed by initStyles when the TUI model is
// built with the user's configured theme. gaugeColor and the huh form theme read
// it directly.
var active = theme.Get("")

// The dashboard styles. They are populated by initStyles (called once at package
// load via init, and again from the TUI model constructor with the configured
// palette), so every color flows from the active palette rather than a literal.
var (
	titleStyle lipgloss.Style
	dimStyle   lipgloss.Style
	okStyle    lipgloss.Style
	errStyle   lipgloss.Style
	// Fixed width + height so the detail panel stays the same size (and centered
	// position) regardless of the selection — managed VMs and discovered guests
	// have a different number of fields.
	panelStyle lipgloss.Style
	// metricsPanelStyle is the global metrics box joined to the right of the detail
	// panel. Same look as panelStyle; Width 38 inner (+2 padding +2 border = 40
	// outer) so detail(48) + gap(1) + metrics(40) == the table width (89) — the
	// panel's right edge lines up with the tables and the centered block isn't
	// widened. Height 9 matches panelStyle so JoinHorizontal(Top) aligns both.
	metricsPanelStyle lipgloss.Style
	// Section/table styles for the manually-rendered Managed/Discovered tables.
	sectionTitleStyle lipgloss.Style
	tableHeaderStyle  lipgloss.Style
	tableRuleStyle    lipgloss.Style
	// Bar styles carry the modal background so the bar line reads as one solid
	// piece (lipgloss does not refill the background behind already-styled spans,
	// which otherwise looks like selected text).
	barOnStyle  lipgloss.Style
	barOffStyle lipgloss.Style

	// headingStyle is the brightest text role (running guest names, panel
	// headings) — brightness alone reads as emphasis, no bold.
	headingStyle lipgloss.Style
	// accentStyle is a plain (non-bold) accent foreground — the selection gutter bar.
	accentStyle lipgloss.Style
	// warnStyle is the warning foreground (the drift "!" marker on a name).
	warnStyle lipgloss.Style
	// faintStyle is the most muted text role (ids, table headers, footer
	// descriptions).
	faintStyle lipgloss.Style
	// lineStyle colors meter tracks and stronger rules.
	lineStyle lipgloss.Style
	// selBG is the selected-row background, a concrete pre-blended color composed
	// into per-cell styles (not a Style itself, since it's combined with other
	// foregrounds cell by cell).
	selBG lipgloss.Color

	// modalStyle is the floating wizard window centered over the dashboard.
	modalStyle lipgloss.Style
	// Modal-scoped text styles — same palette as the dashboard but with the modal
	// background, so text inside the window doesn't render on islands of the
	// terminal's default background.
	modalTitleStyle lipgloss.Style
	mLabelStyle     lipgloss.Style
	mDimStyle       lipgloss.Style
	mErrStyle       lipgloss.Style

	// outputBoxStyle is the embedded "terminal": dark background, light text, a
	// clearly differentiated box inside the modal.
	outputBoxStyle lipgloss.Style
)

// initStyles (re)builds every dashboard style from the given semantic palette and
// records it as the active palette. Called at package load with the default
// palette and from the TUI model constructor with theme.Get(cfg.Theme).
func initStyles(p theme.Palette) {
	active = p

	// The title text next to the accent "❯" prompt char — bright, like the
	// landing page's hero title.
	titleStyle = lipgloss.NewStyle().Foreground(p.Heading).Bold(true)
	dimStyle = lipgloss.NewStyle().Foreground(p.Dim)
	okStyle = lipgloss.NewStyle().Foreground(p.Good)
	errStyle = lipgloss.NewStyle().Foreground(p.Bad)
	// Height is fixed and shared: the two panels are joined side by side, so they
	// must agree or their bottom borders don't line up. It fits the tallest panel
	// (a managed VM: name/state/cpu/ram/spec/model/net/user/prov/drift).
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(p.LineSoft).Padding(0, 1).Width(46).Height(10)
	metricsPanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(p.LineSoft).Padding(0, 1).Width(metricsInnerW).Height(10)
	sectionTitleStyle = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	tableHeaderStyle = lipgloss.NewStyle().Foreground(p.Faint)
	tableRuleStyle = lipgloss.NewStyle().Foreground(p.LineSoft)
	barOnStyle = lipgloss.NewStyle().Foreground(p.Accent).Background(p.ModalBG)
	barOffStyle = lipgloss.NewStyle().Foreground(p.Track).Background(p.ModalBG)

	headingStyle = lipgloss.NewStyle().Foreground(p.Heading)
	accentStyle = lipgloss.NewStyle().Foreground(p.Accent)
	warnStyle = lipgloss.NewStyle().Foreground(p.Warn)
	faintStyle = lipgloss.NewStyle().Foreground(p.Faint)
	lineStyle = lipgloss.NewStyle().Foreground(p.Line)
	selBG = p.SelBG

	modalStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.Accent).
		Background(p.ModalBG).
		Padding(1, 2).
		Width(60)
	modalTitleStyle = lipgloss.NewStyle().Foreground(p.Accent).Background(p.ModalBG).Bold(true)
	mLabelStyle = lipgloss.NewStyle().Foreground(p.Accent).Background(p.ModalBG).Bold(true)
	mDimStyle = lipgloss.NewStyle().Foreground(p.Dim).Background(p.ModalBG)
	// Modal-scoped Bad: errStyle has no ModalBG, so it would paint the failure line
	// on a background island inside the run window.
	mErrStyle = lipgloss.NewStyle().Foreground(p.Bad).Background(p.ModalBG)

	outputBoxStyle = lipgloss.NewStyle().
		Background(p.OutBG).
		Foreground(p.OutFG).
		Padding(0, 1).
		Width(56)
}

func init() { initStyles(active) }

// cpuModelCell renders a guest's QEMU CPU model. Containers share the host kernel
// and have none, so they get a dash rather than a misleading default.
func cpuModelCell(vm *state.VMSpec) string {
	if vm.IsLXC() {
		return dimStyle.Render("—")
	}
	return config.CPUTypeOrDefault(vm.CPUType)
}
