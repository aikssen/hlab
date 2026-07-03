package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/aikssen/hlab/internal/config"
	"github.com/aikssen/hlab/internal/sshutil"
)

var (
	addKeyFlag      string
	addPasswordFlag string
)

var vmAddSSHKeyCmd = &cobra.Command{
	Use:   "add-ssh-key <name|id>",
	Short: "Add an SSH public key to an existing VM (live guest + declaration)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddSSHKey,
}

var ctAddSSHKeyCmd = &cobra.Command{
	Use:   "add-ssh-key <name|id>",
	Short: "Add an SSH public key to an existing container (live guest + declaration)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddSSHKey,
}

func init() {
	const keyUsage = "public key: a path to a .pub file or a literal 'ssh-ed25519 AAAA...' string (prompts from ~/.ssh when omitted)"
	vmAddSSHKeyCmd.Flags().StringVar(&addKeyFlag, "key", "", keyUsage)
	ctAddSSHKeyCmd.Flags().StringVar(&addKeyFlag, "key", "", keyUsage)

	const pwUsage = "root password for a keyless LXC's first key via the Proxmox console (only needed/used when the container has no SSH key and no stored password; prompts when omitted on a terminal)"
	vmAddSSHKeyCmd.Flags().StringVar(&addPasswordFlag, "password", "", pwUsage)
	ctAddSSHKeyCmd.Flags().StringVar(&addPasswordFlag, "password", "", pwUsage)

	vmCmd.AddCommand(vmAddSSHKeyCmd)
	ctCmd.AddCommand(ctAddSSHKeyCmd)
}

// runAddSSHKey implements `hlab vm add-ssh-key` / `hlab ct add-ssh-key`: it
// installs a public key on the live guest's authorized_keys over SSH (immediate
// effect — cloud-init only injects keys at first boot) and then persists it to
// the declaration + reconciles Terraform via Engine.AddSSHKey. Shared between
// both command groups, like runUpdate: the engine routes by the loaded
// declaration's type, and the live guest is reached as the same user
// `hlab {vm,ct} ssh` connects as (the configured user for a VM, root for a
// container).
func runAddSSHKey(_ *cobra.Command, args []string) error {
	cfg, store, runner, err := loadStack()
	if err != nil {
		return err
	}
	name, err := resolveVMName(store, args[0])
	if err != nil {
		return err
	}
	vm, err := store.Load(name)
	if err != nil {
		return err
	}

	pub, err := resolveSSHPublicKey(addKeyFlag)
	if err != nil {
		return err
	}

	eng := newEngine(cfg, store, runner)

	// A guest with no key already trusted can't be reached over SSH (hlab uses key
	// auth only, never a password). Recovery differs by kind: a keyless VM's first
	// key goes in through the QEMU guest agent (runs as root inside the VM, no login
	// needed); a keyless LXC container has no guest agent, so hlab seeds its first
	// key over the Proxmox console (the root password works there even though sshd
	// refuses it).
	if len(vm.SSHKeys) == 0 {
		if !vm.IsLXC() {
			if err := runStep(
				fmt.Sprintf("No SSH access to %s — injecting the key via the QEMU guest agent…", name),
				func() error { return eng.InjectSSHKeyViaAgent(vm, pub) },
			); err != nil {
				return err
			}
			fmt.Printf("\n✓ Added the SSH key to %q via the QEMU guest agent — SSH now works.\n", name)
			return nil
		}
		// The console login needs the container's root password. It normally comes
		// from the machine-local (gitignored, never versioned) secrets file, so it is
		// absent for a container created elsewhere or by an older hlab. Fall back to
		// --password, then to an interactive prompt; a non-interactive run with no
		// password errors with the actionable guidance instead of hanging.
		password, err := eng.StoredCTPassword(name)
		if err != nil {
			return err
		}
		if password == "" {
			password = strings.TrimSpace(addPasswordFlag)
		}
		if password == "" {
			if !isatty.IsTerminal(os.Stdin.Fd()) {
				return sshutil.KeylessAddKeyError(name, true)
			}
			if password, err = promptRootPassword(name); err != nil {
				return err
			}
			if strings.TrimSpace(password) == "" {
				return sshutil.KeylessAddKeyError(name, true)
			}
		}
		if err := runStep(
			fmt.Sprintf("No SSH access to %s — injecting the key via the Proxmox console…", name),
			func() error { return eng.InjectSSHKeyViaConsoleWithPassword(vm, pub, password) },
		); err != nil {
			return err
		}
		fmt.Printf("\n✓ Added the SSH key to %q via the Proxmox console — SSH now works.\n", name)
		return nil
	}

	ip := eng.ResolveIP(vm)
	if ip == "" {
		return fmt.Errorf("no IP address known for %q yet", name)
	}

	// Install the key on the live guest first (the whole point — immediate
	// effect), then record it and reconcile Terraform so nothing drifts.
	if err := runStep(
		fmt.Sprintf("Adding SSH key to %s (%s)…", name, ip),
		func() error { return sshutil.AppendAuthorizedKey(vm.Username, ip, pub) },
	); err != nil {
		return err
	}
	if err := eng.AddSSHKey(vm, pub); err != nil {
		return err
	}

	fmt.Printf("\n✓ Added the SSH key to %q.\n", name)
	return nil
}

// promptRootPassword asks the operator for a keyless container's root password so
// hlab can log in to the Proxmox console to seed the first SSH key. Used only when
// no password is stored and none was passed with --password.
func promptRootPassword(name string) (string, error) {
	var pw string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(fmt.Sprintf("Root password for %s (Proxmox console login)", name)).
			Description("This container has no SSH key hlab can use and no stored password, so hlab logs into the Proxmox console as root to install the first key.").
			EchoMode(huh.EchoModePassword).
			Value(&pw),
	)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return "", err
	}
	return pw, nil
}

// resolveSSHPublicKey turns the --key argument into a public-key string. An
// empty arg falls back to an interactive picker over ~/.ssh. A non-empty arg is
// used verbatim when it already looks like a public key; otherwise it is read
// as a path to a .pub file (a leading ~ is expanded).
func resolveSSHPublicKey(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return pickSSHPublicKey()
	}
	if looksLikeSSHPublicKey(arg) {
		return arg, nil
	}
	path := expandUser(arg)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading key file %q: %w", arg, err)
	}
	key := strings.TrimSpace(string(data))
	if !looksLikeSSHPublicKey(key) {
		return "", fmt.Errorf("%q does not contain an SSH public key", arg)
	}
	return key, nil
}

// looksLikeSSHPublicKey reports whether s starts with a known OpenSSH public-key
// algorithm prefix — enough to distinguish a literal key from a filesystem path.
func looksLikeSSHPublicKey(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"ssh-", "ecdsa-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// expandUser expands a leading ~ (or ~/) to the current user's home directory.
func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// pickSSHPublicKey lets the operator choose one of the public keys in ~/.ssh,
// matching how `hlab setup` presents key choices (config.ScanSSHKeys + a huh
// select). Returns the key contents.
func pickSSHPublicKey() (string, error) {
	keys, err := config.ScanSSHKeys()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("no public keys found in ~/.ssh — pass --key with a path or a literal key")
	}
	opts := make([]huh.Option[string], 0, len(keys))
	for _, k := range keys {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", k.Name, k.Path), k.Pub))
	}
	var chosen string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("SSH public key to add").
			Options(opts...).
			Value(&chosen),
	)).WithTheme(cmdHuhTheme()).Run(); err != nil {
		return "", err
	}
	if strings.TrimSpace(chosen) == "" {
		return "", fmt.Errorf("no key selected")
	}
	return chosen, nil
}
