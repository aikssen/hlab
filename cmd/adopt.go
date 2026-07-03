package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/proxmox"
	"github.com/aikssen/hlab/internal/state"
)

var (
	adoptName string
	adoptUser string
	adoptYes  bool
)

var vmAdoptCmd = &cobra.Command{
	Use:   "adopt <vmid|name>",
	Short: "Adopt a discovered (unmanaged) VM into hlab",
	Args:  cobra.ExactArgs(1),
	RunE:  runVMAdopt,
}

var ctAdoptCmd = &cobra.Command{
	Use:   "adopt <vmid|name>",
	Short: "Adopt a discovered (unmanaged) container into hlab",
	Args:  cobra.ExactArgs(1),
	RunE:  runCTAdopt,
}

func init() {
	vf := vmAdoptCmd.Flags()
	vf.StringVar(&adoptName, "name", "", "declaration name to adopt under (default: kebab-cased live name)")
	vf.StringVar(&adoptUser, "user", "", "SSH username when the VM has no ciuser set (default: root)")
	vf.BoolVarP(&adoptYes, "yes", "y", false, "skip the confirmation prompt")

	cf := ctAdoptCmd.Flags()
	cf.StringVar(&adoptName, "name", "", "declaration name to adopt under (default: kebab-cased live name)")
	cf.BoolVarP(&adoptYes, "yes", "y", false, "skip the confirmation prompt")

	vmCmd.AddCommand(vmAdoptCmd)
	ctCmd.AddCommand(ctAdoptCmd)
}

func runVMAdopt(_ *cobra.Command, args []string) error {
	return runAdopt(args[0], "qemu")
}

func runCTAdopt(_ *cobra.Command, args []string) error {
	return runAdopt(args[0], "lxc")
}

// runAdopt implements `hlab vm adopt` / `hlab ct adopt`: it resolves the
// discovered guest, synthesizes a declaration from its live config, shows the
// operator what will be adopted (plus any warnings about what the first
// managed apply will change), confirms, then hands off to the engine to
// import it into Terraform state. Shared between both command groups —
// wantKind picks "qemu" or "lxc" so e.g. a container id typed under `vm
// adopt` fails with an actionable "use `hlab ct adopt <id>`" message.
func runAdopt(arg, wantKind string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	pm := proxmox.New(cfg.ProxmoxURL, cfg.TokenID, cfg.TokenSecret, cfg.Insecure)
	eng := engine.New(cfg, store, runner, pm)

	g, err := eng.FindAdoptable(arg, wantKind)
	if err != nil {
		return err
	}

	spec, warnings, err := eng.BuildAdoptSpec(*g, engine.AdoptOptions{Name: adoptName, Username: adoptUser})
	if err != nil {
		return err
	}

	// The guest-agent/container-namespace read only works while the guest is
	// running; otherwise fall back to whatever the synthesized declaration
	// already carries (a static ipconfig0/net0, or none for DHCP).
	ip := ""
	if g.Status == "running" {
		ip = pm.GuestIPv4(g.Node, g.Type, g.VMID)
	}
	if ip == "" {
		ip = engine.DeclaredIP(spec)
	}

	fmt.Println(renderAdoptSummary(spec, ip))
	for _, w := range warnings {
		fmt.Printf("! %s\n", w)
	}

	if !adoptYes {
		confirm := false
		if err := confirmf(&confirm, "Adopt %d (%s on %s) as %q? This never modifies the live guest.", g.VMID, g.Name, g.Node, spec.Name); err != nil {
			return err
		}
		if !confirm {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	var drift string
	if err := runStep(
		fmt.Sprintf("Adopting %s… (-v for details)", spec.Name),
		func() error { var e error; drift, e = eng.Adopt(spec); return e },
	); err != nil {
		return err
	}

	fmt.Printf("\n✓ Adopted %q — now managed by hlab.\n", spec.Name)
	if drift != "" {
		fmt.Println("\n! in-place drift between the declaration and the live guest — the next apply will reconcile it:")
		for line := range strings.SplitSeq(strings.TrimRight(drift, "\n"), "\n") {
			fmt.Println("    " + line)
		}
	}

	verb := "vm"
	if spec.IsLXC() {
		verb = "ct"
	}
	fmt.Println("\n  Next steps:")
	fmt.Printf("    hlab %s provision %d    # install software / dotfiles\n", verb, spec.VMID)
	fmt.Printf("    hlab %s ssh %d           # connect\n", verb, spec.VMID)
	return nil
}

// renderAdoptSummary draws a summary box of what `adopt` will bring under
// management. It's a dedicated block rather than a reuse of renderResult:
// adopt needs Type/Storage/Bridge (not shown by renderResult) and has no
// login/software/dotfiles selection to report.
func renderAdoptSummary(spec *state.VMSpec, ip string) string {
	label := lipgloss.NewStyle().Foreground(cmdPalette.Accent).Width(11)
	typ := "vm"
	if spec.IsLXC() {
		typ = "lxc"
	}
	rows := [][2]string{
		{"Name", spec.Name},
		{"ID / Node", fmt.Sprintf("%d  %s", spec.VMID, spec.Node)},
		{"Type", typ},
		{"CPU / RAM", fmt.Sprintf("%d cores / %s", spec.Cores, ramDisplay(spec))},
		{"Disk", fmt.Sprintf("%d GB", spec.DiskGB)},
		{"Storage", spec.Storage},
		{"Bridge", spec.Bridge},
		{"IP", ipOrDash(ip)},
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Adopt " + spec.Name))
	b.WriteString("\n\n")
	for _, r := range rows {
		b.WriteString(label.Render(r[0]))
		b.WriteString(r[1])
		b.WriteByte('\n')
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cmdPalette.Accent).
		Padding(0, 2).
		Render(strings.TrimRight(b.String(), "\n"))
}
