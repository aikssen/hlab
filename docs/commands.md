# Command & flag reference

Full reference for every `hlab` subcommand and its flags. For the friendly
overview, see the [README](../README.md).

VM and container subcommands accept a **name or a numeric ID**.

## Commands

| Command | What it does |
|---------|--------------|
| `hlab` | Launch the dashboard TUI: manage VMs and containers (create/provision/ssh/destroy/setup) in one app. |
| `hlab setup` | Configure connection/defaults. `--add-node`, `--add-ssh-key` to extend. |
| `hlab doctor` | Check terraform, Proxmox reachability, config. |
| `hlab plan [name\|id]` | Read-only drift report: runs `terraform plan` across the managed fleet (or one guest) and shows which guests have diverged from their declaration. Never applies. |
| `hlab theme [name]` | List themes (marking the active one) or switch to `name`. See [themes](themes.md). |
| `hlab version` | Print the version and build (e.g. `hlab v0.1.0-1a2b3c4`). |

### VMs (`hlab vm …`)

| Command | What it does |
|---------|--------------|
| `hlab vm create` | Create the VM only: wizard → declaration → `terraform apply`. `--dry-run` to plan only. |
| `hlab vm list` | List managed VMs, including what was provisioned (software + dotfiles). |
| `hlab vm show <name\|id>` | Show one VM's details and what was provisioned. |
| `hlab vm destroy <name\|id>` | Destroy the VM and remove its declaration (`--yes` to skip confirm). |
| `hlab vm provision <name\|id>` | Choose and install software (Ansible). `--software a,b` skips the prompt; include `dotfiles` in the list to install your terminal environment. |
| `hlab vm ssh <name\|id>` | Open an interactive SSH session to the VM. |
| `hlab vm start <name\|id>` | Power on the VM. |
| `hlab vm stop <name\|id>` | Graceful guest shutdown (`--force` for a hard stop). |
| `hlab vm reboot <name\|id>` | Graceful guest reboot. |
| `hlab vm migrate <name\|id> --to <node>` | Migrate the VM to another cluster node, keeping its disk and VM id (online when running). |
| `hlab vm resize <name\|id>` | Change CPU / RAM / disk (`--cores`, `--memory`, `--disk`, `--plan`; disk grows only). |
| `hlab vm snapshot <name\|id> <snap>` | Snapshot the VM (`--description`, `--ram` for live memory). |
| `hlab vm snapshots <name\|id>` | List the VM's snapshots. |
| `hlab vm rollback <name\|id> <snap>` | Roll the VM back to a snapshot (`--yes` to skip confirm). |
| `hlab vm snapshot-delete <name\|id> <snap>` | Delete a snapshot (`--yes` to skip confirm). |
| `hlab vm adopt <vmid\|name>` | Bring a discovered (unmanaged) VM under hlab's control (`--name`, `--user`, `-y/--yes`). Never modifies the live VM — see [Adopting existing guests](#adopting-existing-guests). |
| `hlab vm update <name\|id>` | Re-provision the VM idempotently, using its saved software/dotfiles selection (`--upgrade` also apt-upgrades, upgrades mise runtimes, re-pulls dotfiles, and self-updates CLI tools). |
| `hlab vm add-ssh-key <name\|id>` | Add an SSH public key to an existing VM — installs it on the live guest's `authorized_keys` immediately and records it in the declaration. See [SSH keys](ssh-keys.md). |

### LXC containers (`hlab ct …`)

| Command | What it does |
|---------|--------------|
| `hlab ct create` | Create an LXC container: wizard → declaration → `terraform apply`. Flags `--template-file`, `--node`, `--plan`, `--unprivileged`, `--password` (required), … skip the wizard. See [LXC notes](lxc.md). |
| `hlab ct list` | List managed containers. |
| `hlab ct show <name\|id>` | Show one container's details. |
| `hlab ct start\|stop\|reboot\|destroy\|provision\|ssh <name\|id>` | Same lifecycle/day-2 verbs as VMs, for containers. |
| `hlab ct snapshot\|snapshots\|rollback\|snapshot-delete <name\|id> [snap]` | Snapshot a container (no RAM state — containers have none). |
| `hlab ct resize <name\|id>` | Change a container's CPU / RAM / disk (`--cores`, `--memory`, `--disk`, `--plan`; disk grows only). |
| `hlab ct migrate <name\|id> --to <node>` | Migrate a container to another node (via the Proxmox API; a running one is restarted). |
| `hlab ct adopt <vmid\|name>` | Bring a discovered (unmanaged) container under hlab's control (`--name`, `-y/--yes`). Never modifies the live container — see [Adopting existing guests](#adopting-existing-guests). |
| `hlab ct update <name\|id>` | Same as `hlab vm update`, for containers. |
| `hlab ct add-ssh-key <name\|id>` | Same as `hlab vm add-ssh-key`, for containers (installed for root over SSH). See [SSH keys](ssh-keys.md). |

## Flags

### Global (any command)

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Show the underlying Terraform/Ansible output (hidden by default). Repeat (`-vv`) for extra detail. |
| `-h, --help` | Help for the command. |

### `hlab setup`

Interactive by default. Passing `--url` runs it non-interactively.

| Flag | Description |
|------|-------------|
| `--url` | Proxmox URL, e.g. `https://proxmox.example:8006/` (enables non-interactive setup). |
| `--token-id` | API token ID, e.g. `hlab@pve!hlab`. |
| `--token-secret` | API token secret. |
| `--node` | Default node. |
| `--storage` | Default storage (default `local-lvm`). |
| `--bridge` | Default network bridge (default `vmbr0`). |
| `--template` | Default template name to preselect in the wizard. |
| `--gateway` | Default gateway for static IPs, e.g. `192.168.1.1`. |
| `--cidr` | Default subnet prefix, e.g. `24`. |
| `--ssh-key` | SSH key name(s) from `~/.ssh` (filename without `.pub`) to enable. Repeatable. |
| `--dotfiles-repo` | Dotfiles repo SSH URL (e.g. `git@github.com:you/dotfiles.git`). Setting it enables the `dotfiles` provisioning option (listed **first** in the software checklist); leaving it unset hides that option. See [dotfiles](dotfiles.md). |
| `--insecure` | Skip TLS verification (self-signed certs). |
| `--add-node <name>` | Add a node to an existing config (incremental). |
| `--add-ssh-key` | Scan `~/.ssh` and add a public key to an existing config (incremental). |

### `hlab vm create`

Create offers **preconfigured plans** (KVM1/KVM2/KVM4/KVM8 by default — pick a
size instead of typing cores/memory/disk), or **Custom** for manual specs. The
plans live in a user-editable `~/.hlab/plans.yaml` (seeded on first use);
edit it to change the offered sizes. `disk_gb` must be ≥ the template size.

Runs the interactive wizard. Passing `--name` skips the wizard (non-interactive);
unset values fall back to the configured defaults / suggestions.

| Flag | Description |
|------|-------------|
| `--name` | VM name/hostname (setting this skips the wizard). |
| `--node` | Cluster node to create on. Falls back to `default_node` from config, then to the node holding the template. |
| `--vmid` | VM ID. |
| `--template` | Template name to clone (default: configured default template). The VM lands on the node chosen by `--node`/`default_node`, among the nodes holding the template. |
| `--template-id` | Template VM ID to clone (overrides `--template`). |
| `--plan` | Preconfigured plan (e.g. `KVM2`); overrides `--cores`/`--memory`/`--disk`. |
| `--cores` | CPU cores (default `2`). |
| `--memory` | Memory in GB (default `4`). |
| `--disk` | Disk size in GB (default `16`; bumped up to the template size if smaller). |
| `--dhcp` | Use DHCP (default `true`; set `--dhcp=false` for static). |
| `--ip` | Static IPv4 with CIDR, e.g. `192.168.1.50/24` (default: suggested IP). |
| `--gateway` | Static gateway (default: configured gateway). |
| `--dns` | DNS servers (static). Repeatable / comma-separated. |
| `--user` | Administrative username (default: the last username you used for a VM create, else your OS username, or `admin` if it can't be determined). |
| `--password` | cloud-init password (**required** — see [SSH keys](ssh-keys.md)). |
| `--ssh-key` | SSH key name to inject (default: configured default). |
| `--dry-run` | Show the Terraform plan without applying. |

### `hlab vm provision <name|id>`

Interactive by default (a single software checklist). Passing `--software` skips the
prompt (non-interactive).

| Flag | Description |
|------|-------------|
| `--software` | Software keys to install, comma-separated (skips the prompt). Include `dotfiles` to install your terminal environment. |

Available `--software` keys: `docker`, `podman`, `k3s`, `node`, `go`, `python`,
`rust`, `claude-code`, `opencode`, `hermes`, and `dotfiles` (see
`assets/additional-software.yaml`). **Dotfiles is a catalog entry like any other**,
but it only appears in the checklist (and is only installable) when a
`dotfiles_repo` is configured — see [dotfiles](dotfiles.md).

### `hlab vm destroy <name|id>`

| Flag | Description |
|------|-------------|
| `-y, --yes` | Skip the confirmation prompt. |

### `hlab vm stop <name|id>`

| Flag | Description |
|------|-------------|
| `--force` | Hard stop (cut power) instead of a graceful guest shutdown. |

`hlab vm list`, `hlab vm show`, `hlab vm ssh`, `hlab vm start`, `hlab vm reboot`
and `hlab version` take no command-specific flags.

### `hlab vm resize` / `hlab ct resize`

| Flag | Description |
|------|-------------|
| `--cores` | New CPU core count. |
| `--memory` | New memory (VMs in GB; containers GB-default, `M`/`MB` suffix for MB). |
| `--disk` | New disk size (grows only — a smaller value is rejected). |
| `--plan` | Apply a preconfigured plan's sizing instead of individual flags. |

### `hlab vm migrate` / `hlab ct migrate`

| Flag | Description |
|------|-------------|
| `--to <node>` | Target cluster node (required). |

### `hlab vm add-ssh-key` / `hlab ct add-ssh-key`

| Flag | Description |
|------|-------------|
| `--key` | A `.pub` path or a literal key. Prompts from `~/.ssh` when omitted. |
| `--password` | Root/admin password for keyless injection (containers over the console; see [SSH keys](ssh-keys.md)). |

### `hlab ct create`

Same shape as `hlab vm create`, plus:

| Flag | Description |
|------|-------------|
| `--template-file` | Container template volid (`vztmpl`), e.g. `local:vztmpl/debian-12-standard_…tar.zst`. |
| `--unprivileged` | Create an unprivileged container (default). |
| `--password` | Root password (**required**). |

See [LXC notes](lxc.md) for template preparation and container specifics.

## Adopting existing guests

`hlab vm adopt` / `hlab ct adopt` (or `a` in the TUI, on a **Discovered** guest)
bring a VM or container that already exists in Proxmox — but wasn't created by
hlab — under hlab's management: it reads the live config, builds a declaration
from it, and imports it into Terraform state, so it gains `provision`/`ssh`/day-2
ops just like a guest hlab created.

This **never modifies or destroys the live guest**. If the guest's shape can't be
adopted safely (extra disks/NICs/mount points, a non-`scsi0` boot disk) or the
first plan after import would force a replace, the adoption is rolled back and the
guest is left exactly as found. Harmless in-place drift (e.g. the guest agent not
yet enabled, or no static IP configured) is adopted and reported as a warning —
the next `provision`/apply reconciles it.
