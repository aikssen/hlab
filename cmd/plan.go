package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/state"
)

var planCmd = &cobra.Command{
	Use:   "plan [name|id]",
	Short: "Detect drift between managed guests and their hlab declaration",
	Long: "Runs a read-only `terraform plan` across the whole managed fleet (or a single\n" +
		"guest, when given a name/id) and reports which guests have drifted from their\n" +
		"hlab declaration — e.g. cores/RAM/disk/node changed directly in the Proxmox UI.\n" +
		"It never applies; reconcile with `hlab vm/ct resize`, `migrate`, or by re-running\n" +
		"`create` — plan is a report, not a mutation.",
	Args: cobra.MaximumNArgs(1),
	RunE: runPlan,
}

func init() {
	rootCmd.AddCommand(planCmd)
}

func runPlan(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)

	// planOKStyle / planDriftStyle color the summary line only. Coloring the STATE
	// cell inside the tabwriter table was tried and dropped: tabwriter sizes
	// columns by raw byte length, and lipgloss color codes for different colors
	// (Good vs Bad) aren't the same length, so colored STATE cells throw off
	// alignment of the DETAIL column that follows. Keeping STATE plain in the
	// table and coloring only the summary line below it stays correct. Built here
	// (after loadStack) so they pick up the configured theme.
	planOKStyle := lipgloss.NewStyle().Foreground(cmdPalette.Good)
	planDriftStyle := lipgloss.NewStyle().Foreground(cmdPalette.Bad)

	// Scope the plan to a single guest when named, so `hlab plan <name>` runs a
	// targeted `terraform plan` instead of the whole fleet.
	var targets []*state.VMSpec
	title := "Planning drift across the fleet… (-v streams terraform)"
	if len(args) == 1 {
		name, rerr := resolveVMName(store, args[0])
		if rerr != nil {
			return rerr
		}
		spec, lerr := store.Load(name)
		if lerr != nil {
			return fmt.Errorf("%q is not managed by hlab (nothing to plan)", name)
		}
		targets = []*state.VMSpec{spec}
		title = fmt.Sprintf("Planning drift for %s… (-v streams terraform)", name)
	}

	var statuses []engine.DriftStatus
	if err := runStep(
		title,
		func() error { var e error; statuses, e = eng.DetectDrift(targets...); return e },
	); err != nil {
		return err
	}

	if len(statuses) == 0 {
		fmt.Println("No managed guests yet — nothing to plan.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSTATE\tDETAIL")
	drifted := 0
	for _, s := range statuses {
		if s.State != "in-sync" {
			drifted++
		}
		detail := "-"
		if len(s.Attrs) > 0 {
			detail = strings.Join(s.Attrs, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Kind, s.State, detail)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	switch {
	case drifted == 0 && len(statuses) == 1:
		fmt.Println(planOKStyle.Render("✓ in sync."))
	case drifted == 0:
		fmt.Println(planOKStyle.Render(fmt.Sprintf("✓ all %d managed guests in sync.", len(statuses))))
	default:
		fmt.Println(planDriftStyle.Render(fmt.Sprintf("%d of %d managed guests drifted.", drifted, len(statuses))))
	}
	return nil
}
