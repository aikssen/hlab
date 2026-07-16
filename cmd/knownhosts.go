package cmd

import (
	"fmt"
	"net"
	"os"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/engine"
	"github.com/aikssen/hlab/internal/sshutil"
	"github.com/aikssen/hlab/internal/state"
)

var knownHostsCmd = &cobra.Command{
	Use:   "known-hosts",
	Short: "Manage the SSH host keys recorded for managed guests",
}

var knownHostsCleanCmd = &cobra.Command{
	Use:   "clean [name|id|ip]",
	Short: "Remove stale SSH host keys left behind by destroyed guests",
	Long: "Removes the known_hosts entries recorded for a guest's address, so a recycled\n" +
		"address doesn't greet the next ssh with \"REMOTE HOST IDENTIFICATION HAS CHANGED\".\n\n" +
		"`hlab {vm,ct} create` and `destroy` already do this for the guests they touch, so\n" +
		"this is the escape hatch for the rest: an address whose guest was destroyed from\n" +
		"another machine, outside hlab, or before hlab cleaned up after itself.\n\n" +
		"A name or id is resolved to the guest's address; a bare IP is cleaned as given —\n" +
		"which is what lets you clean up after a guest hlab no longer has a declaration for.\n\n" +
		"--all instead walks every managed guest and removes only the entries that\n" +
		"provably disagree with the host key the guest currently presents; entries that\n" +
		"match, and guests that can't be reached to check, are left alone.",
	Args: cobra.MaximumNArgs(1),
	RunE: runKnownHostsClean,
}

var knownHostsAll bool

func init() {
	knownHostsCleanCmd.Flags().BoolVar(&knownHostsAll, "all", false,
		"check every managed guest, removing only entries that disagree with its live host key")
	knownHostsCmd.AddCommand(knownHostsCleanCmd)
	rootCmd.AddCommand(knownHostsCmd)
}

func runKnownHostsClean(_ *cobra.Command, args []string) error {
	if knownHostsAll && len(args) > 0 {
		return fmt.Errorf("--all already covers every managed guest — drop the %q argument", args[0])
	}
	if !knownHostsAll && len(args) == 0 {
		return fmt.Errorf("name an address to clean (a guest name, id, or IP), or pass --all to check every managed guest")
	}

	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	eng := newEngine(cfg, store, runner)

	if knownHostsAll {
		return cleanAllKnownHosts(eng, store)
	}
	return cleanOneKnownHost(eng, store, args[0])
}

// cleanOneKnownHost drops every recorded key for a single address. It does not
// check the address against its live key first: the operator named this target
// explicitly, which is the whole point of the escape hatch — it has to work for a
// guest that is already gone and therefore cannot be scanned.
func cleanOneKnownHost(eng *engine.Engine, store *state.Store, arg string) error {
	ip := arg
	// A bare IP is used verbatim, and deliberately without consulting the store:
	// the addresses most in need of cleaning belong to guests whose declaration no
	// longer exists.
	if net.ParseIP(arg) == nil {
		name, rerr := resolveVMName(store, arg)
		if rerr != nil {
			return fmt.Errorf("%w (pass an IP directly to clean an address hlab no longer manages)", rerr)
		}
		spec, lerr := store.Load(name)
		if lerr != nil {
			return lerr
		}
		if ip = eng.ResolveIP(spec); ip == "" {
			return fmt.Errorf("could not work out the address of %q — pass its IP directly", name)
		}
	}

	// Look before removing, purely so the report is honest: ssh-keygen -R is
	// idempotent and says nothing about whether it actually matched, and claiming
	// to have cleaned an address that had no entry is a lie that costs trust the
	// next time this command says it did something.
	had := len(sshutil.RecordedHostKeys(ip))
	if err := sshutil.Forget(ip); err != nil {
		return err
	}
	good := lipgloss.NewStyle().Foreground(cmdPalette.Good)
	if had == 0 {
		fmt.Println(good.Render(fmt.Sprintf("✓ nothing recorded for %s — already clean.", ip)))
		return nil
	}
	fmt.Println(good.Render(
		fmt.Sprintf("✓ cleaned %d known_hosts %s for %s.", had, pluralEntries(had), ip)))
	return nil
}

// cleanAllKnownHosts walks the managed fleet and removes only entries it can
// prove wrong: those whose recorded key contradicts the key the guest presents
// right now. A guest that can't be scanned is skipped rather than cleaned —
// unreachable is not evidence of staleness, and a correct entry is worth more
// than a tidy report.
func cleanAllKnownHosts(eng *engine.Engine, store *state.Store) error {
	vms, err := store.List()
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("No managed guests yet — nothing to clean.")
		return nil
	}

	type result struct{ name, ip, state string }
	var results []result
	cleaned := 0

	if err := runStep("Checking recorded host keys against the live fleet…", func() error {
		for _, vm := range vms {
			ip := eng.ResolveIP(vm)
			if ip == "" {
				results = append(results, result{vm.Name, "-", "no address"})
				continue
			}
			recorded := sshutil.RecordedHostKeys(ip)
			if len(recorded) == 0 {
				results = append(results, result{vm.Name, ip, "not recorded"})
				continue
			}
			live, lerr := sshutil.LiveHostKeys(ip)
			if lerr != nil {
				results = append(results, result{vm.Name, ip, "unreachable (skipped)"})
				continue
			}
			if !sshutil.HostKeysMismatch(recorded, live) {
				results = append(results, result{vm.Name, ip, "ok"})
				continue
			}
			if ferr := sshutil.Forget(ip); ferr != nil {
				results = append(results, result{vm.Name, ip, "failed: " + ferr.Error()})
				continue
			}
			cleaned++
			results = append(results, result{vm.Name, ip, "cleaned (stale)"})
		}
		return nil
	}); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDRESS\tSTATE")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.name, r.ip, r.state)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	if cleaned == 0 {
		fmt.Println(lipgloss.NewStyle().Foreground(cmdPalette.Good).Render(
			"✓ no stale entries — nothing to clean."))
		return nil
	}
	fmt.Println(lipgloss.NewStyle().Foreground(cmdPalette.Good).Render(
		fmt.Sprintf("✓ cleaned %d stale %s.", cleaned, pluralEntries(cleaned))))
	return nil
}

func pluralEntries(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}
