# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.10.5] - 2026-07-23

### Added
- **Docker Compose ships with Docker** — selecting the `docker` software now also
  installs the Docker Compose v2 plugin (`docker compose`) via the
  `docker-compose-plugin` apt package. It is not a separate catalog entry: Compose
  is only ever installed alongside the Docker Engine, never on its own. The
  `get.docker.com` convenience script usually pulls the plugin in already; the
  explicit, idempotent apt task guarantees it regardless.

## [0.10.4] - 2026-07-16

### Added
- **Configurable VM CPU model** — the QEMU CPU model was hardcoded to
  `x86-64-v2-AES` with no way to override it. Pick it now in `hlab setup` or the
  dashboard's setup form (`S`), or pass `hlab setup --cpu-type <model>` for
  scripting; `hlab vm show` and the dashboard's detail panel show a VM's model. It
  is stored as `cpu_type` in `~/.hlab/config.yaml` and can be overridden per-VM in a
  declaration. The offered list is a curated shortlist — one entry per meaningful
  point on the portability-vs-instruction-set axis — filtered by the host's vendor,
  since an Intel model can't start on an AMD host.

  Why it matters: that default is a *portable* baseline, chosen so a VM can
  live-migrate between nodes with different host CPUs — but it exposes AES and
  **not PCLMULQDQ**, and a binary compiled to require that instruction dies at
  startup with SIGILL (found the hard way: Google's Antigravity CLI). Proxmox won't
  let a lone flag be added on top of a model (its `flags` field only takes a
  security/virt subset) and the psABI levels don't help — `x86-64-v3` has AVX2 but
  still no PCLMULQDQ — so the model itself has to change. Pick the oldest one every
  node can present: `EPYC` on an all-AMD cluster, `host` only if you never migrate
  between unlike CPUs. Existing declarations are unaffected: an unset `cpu_type`
  renders no key at all, so Terraform's default applies and nothing drifts.

### Fixed
- **A failed provision no longer makes the declaration lie** — the software
  selection was saved *before* Ansible ran, so the declaration recorded what was
  asked for rather than what is on the guest. A failed run left hlab reporting
  software that was never installed, and a retry with a narrower selection
  overwrote the list, silently dropping software an earlier run really had
  installed (observed: a guest with Coolify running that hlab no longer listed,
  while claiming dotfiles that wasn't there). The selection is now saved only once
  Ansible succeeds; Ansible is idempotent, so a failure leaves the declaration
  untouched and is recovered by re-running provision with the full selection.
- **An empty SSH agent now fails fast, in every caller** — an SSH `dotfiles_repo`
  is cloned from inside the guest over the operator's *forwarded* agent, and SSH
  authenticates with a key however public the repo is, so an agent holding no
  identities cannot clone it. hlab only warned about this, and only in the CLI: the
  dashboard had no check at all and dived into a run guaranteed to die minutes
  later, at the last task, as a bare `exit status 2`. The check moved into the
  engine — next to the existing `dotfiles_repo` one, so CLI and TUI both get it —
  and is now a hard error naming `ssh-add`. It applies only to SSH URLs
  (`git@host:path`, `ssh://`): an `https://` repo clones without an agent and is
  never blocked.
- **The dashboard no longer throws away the reason a run failed** — the progress
  window closed on failure, discarding the log panel that held the tool output, and
  reduced everything to `error: exit status 2` in the status line. It now stays open
  on failure with the output shown, the progress bar stopped, and `esc` closing it
  instead of cancelling.
- **`provision` waits for SSH instead of racing it** — `create` returns as soon as
  the guest agent reports an address, which happens early in boot (and
  `EnsureStaticApplied` may have just rebooted the guest), so `create` followed
  immediately by `provision` — a script, or the dashboard's create→provision chain —
  could die on Ansible's `UNREACHABLE`. Provision now waits for port 22 to accept
  connections first, costing nothing when the guest is already up.
- **`hlab setup --dotfiles-repo <url>` works on its own** — it was only honoured
  inside the `--url` non-interactive path, so on its own it fell through to the
  interactive wizard and, without a TTY, just failed. It is now an incremental flag
  like `--add-node`/`--add-ssh-key`. Its help no longer says "SSH URL": an `https://`
  URL is valid, and for a public repo it is the better choice.

## [0.10.3] - 2026-07-16

### Added
- **hlab cleans up the host keys it leaves behind** — destroying a guest and creating
  another at the same address (recycled test IDs, a small static-IP pool) used to
  leave a stale `known_hosts` entry and greet the next `ssh` with "REMOTE HOST
  IDENTIFICATION HAS CHANGED". hlab was the one recording those entries
  (`accept-new` in `add-ssh-key`, the TOFU prompt behind `{vm,ct} ssh`) and never
  removed them. Now `create` and `destroy` drop the entry for the address they touch
  — best-effort, never failing the operation. This is hooked to *mutations*, never to
  connections: hlab only drops an entry for a guest it just created or destroyed, so
  no man-in-the-middle warning is ever suppressed.
- **`hlab known-hosts clean [name|id|ip]`** — the escape hatch for addresses hlab
  didn't clean itself (guest destroyed from another machine, outside hlab, or before
  this existed). Takes a name/id, or a bare IP for a guest with no declaration left.
  `--all` walks the fleet and removes only entries that provably disagree with the
  guest's live host key (`ssh-keyscan`); matching entries are kept and unreachable
  guests are skipped. See [ssh-keys.md](docs/ssh-keys.md).
- **Coolify in the software catalog** — `--software coolify` (or the provisioning
  checklist) installs [Coolify](https://coolify.io), a self-hosted PaaS, via its
  official installer. It brings its own Docker engine, so it needs no other catalog
  entry. The UI is served on port **8000** (plus **6001** for realtime); the first
  visit redirects to `/register` to create the root user. `hlab {vm,ct} update
  --upgrade` re-runs the installer, which is Coolify's documented upgrade path.
  Coolify asks for 2 cores / 2 GB RAM / 30 GB disk, so size the guest at least
  `KVM2` (VM) or `large` (LXC — `medium`'s 16 GB rootfs is under the 30 GB floor).
  Note that Coolify registers the host as its own first server: it generates a
  keypair under `/data/coolify/ssh/keys` and installs the public half into
  `root`'s `authorized_keys`.

### Fixed
- **A changed host key now says what to do** — `{vm,ct} add-ssh-key` connects with
  `StrictHostKeyChecking=accept-new`, which records an unknown host silently but
  refuses a changed one. That refusal isn't an auth failure, so it fell through to a
  raw ssh dump; it now names the cause and points at `hlab known-hosts clean <ip>`.

## [0.10.2] - 2026-07-05

### Added
- **Editable LXC swap** — swap is now adjustable after creation, not just at
  `ct create` time: the dashboard edit form (`e`) shows a "Swap (MB)" field for
  containers (right after memory), and the CLI gains `hlab ct resize --swap <MB>`.
  Swap isn't part of any plan, so it stays editable whether a named plan or Custom
  is selected. VMs are unaffected — QEMU VMs have no hypervisor-level swap.

### Changed
- **Dashboard shows real VM memory usage** — the RAM gauge in the guest detail
  panel now reflects the guest's own accounting (read from `/proc/meminfo` via the
  QEMU guest agent: `MemTotal - MemAvailable`), instead of the hypervisor's balloon
  figure. The balloon figure counts reclaimable page cache as "used", so a healthy
  Linux VM read near-full (e.g. 89%) while the guest itself was at ~39%. It falls
  back to the balloon figure for containers (no QEMU agent) or when the agent is
  unavailable; the gauge format is unchanged.

## [0.10.1] - 2026-07-04

### Added
- **Windows binaries** — each release now publishes `hlab_windows_amd64.exe` and
  `hlab_windows_arm64.exe`. The Terraform-driven surface works natively on Windows
  (discovery, dashboard TUI, create/destroy, power, snapshots, migrate, adopt,
  drift/`plan`, resize, git-versioned state) given `terraform` and `git` on `PATH`.
  **Note:** software provisioning (`hlab vm provision` / `update`, `ct provision`)
  uses Ansible, which does not run on native Windows — run those under WSL.

### Fixed
- **`hlab vm ssh` / `hlab ct ssh` on Windows** — the interactive SSH launch used
  `syscall.Exec`, a no-op stub on Windows that returned "not supported by windows".
  It now runs `ssh` as a stdio-inherited child on Windows (Win10+ ships the OpenSSH
  client), while keeping process replacement on Unix.

## [0.10.0] - 2026-07-04

### Added
- **Homebrew** — install and update via the `aikssen/tap` Homebrew tap:
  `brew install aikssen/tap/hlab`.
- **`github-dark` theme** — a new built-in truecolor palette matching the hlab.sh
  design tokens, now the default (an unset or unknown `theme:` resolves to it, and
  it lists first in the theme selector). The old ANSI `default` theme, which read
  too close to `mono`, is replaced by **`one-dark`** (Atom's One Dark, the most
  popular IDE color scheme). Themes gain five roles (`heading`, `faint`, `line`,
  `line_soft`, `sel_bg`); existing `~/.hlab/themes.yaml` files keep working —
  omitted new roles fall back to the same theme's resolved
  `text`/`dim`/`track`/`line`/`modal_bg`.

### Changed
- **Dashboard redesign** — the guest tables now lead with a status dot (`●`
  running / `○` stopped) and per-cell colors (bright names for running guests,
  muted ids/details), show a 6-cell braille CPU meter and the declared memory per
  row, and drop the STATUS text column (drift is a warn-colored `!` next to the
  name). The selected row is a subtle background tint with a `▎` accent bar
  instead of a solid block. The detail panel gains the status dot and quieter
  lowercase labels; the cluster panel shows per-node guest counts (`K/N up`) and
  an `online` tag; headers, section titles and the two-tone footer follow the
  same visual language.

## [0.9.0] - 2026-07-03

### Added
- **Installer bundles the runtime toolchain** — `scripts/install.sh` now also
  installs hlab's two runtime tools after hlab itself: **Terraform** (required for
  `hlab {vm,ct} create`) and **Ansible** (only for `hlab {vm,ct} provision`),
  picking the correct build for the detected OS/arch. Terraform is fetched from the
  official HashiCorp release zip (pinned to a known-good version, overridable via
  `HLAB_TERRAFORM_VERSION`); Ansible is installed best-effort via pipx → Homebrew →
  system package manager → `pip3 --user`. An already-present Terraform or Ansible is
  detected and reused. Dependency installation is **non-fatal**: a tool that can't be
  auto-installed only warns (with manual-install guidance) and never blocks the hlab
  binary. New env knobs: `HLAB_TERRAFORM_VERSION` and `HLAB_SKIP_DEPS=1` (skip the
  dependency phase entirely). The installer ends with a short summary of what's
  available.
- **Guest-agent SSH-key injection for keyless VMs** — `hlab vm add-ssh-key` (and
  the dashboard `i` key) can seed the **first** SSH key into a VM created with a
  password but **no key**. Such a VM is unreachable by
  hlab (Ubuntu cloud images ship `PasswordAuthentication no`, so sshd refuses the
  password, and hlab connects with key auth only), so the key can't go in over SSH.
  hlab now injects it through the **QEMU guest agent**, which runs as root inside the
  VM and needs no login (the VM analogue of the container console path — a VM's
  graphical console is VNC, not a text stream): it execs a small `/bin/sh` script
  via `agent/exec` that appends the key to the connection user's
  `~/.ssh/authorized_keys` (the key is fed on stdin via `input-data`, so it is never
  shell-quoted), polls `agent/exec-status` for the result, then persists + reconciles
  the key like any other. Requires `qemu-guest-agent` running in the VM (hlab's
  golden images set `agent=1`) and the **`VM.GuestAgent.Unrestricted`** privilege on
  the API token (`pveum role modify HLab --privs "VM.GuestAgent.Unrestricted"
  --append 1`); a 403 surfaces that exact requirement. LXC containers keep the
  console path (they have no guest agent).
- **Console SSH-key injection for keyless LXC containers** — `hlab ct add-ssh-key`
  (and the dashboard `i` key) can seed the **first** SSH key into a container
  created with **no key** (sshd refuses root password auth, so hlab can't reach it
  over SSH to install one). hlab now drives the same
  **termproxy websocket** the Proxmox web console uses: it opens the console, logs in
  as root with the container's stored root password, appends the key to
  `/root/.ssh/authorized_keys` (verified with a success sentinel), and then persists
  it like any other key — after which normal SSH works. The root password comes from
  the machine-local (gitignored, never versioned) secrets file when it was stored at
  create time; **when it isn't** (container created on another machine or by an older
  hlab) hlab now **prompts** for it (CLI and the TUI `i` form) instead of failing, so
  a keyless container is recoverable as long as you know the root password. A new
  `--password` flag supplies it non-interactively (only used/needed for a keyless
  LXC's first key via the console); a non-TTY run with no password errors with the
  actionable message rather than hanging. Requires the **`VM.Console`** privilege on
  the API token (a 403 surfaces an actionable message). VMs use the guest-agent path
  instead (see above — a VM's console is VNC, not a text stream). New dependency:
  `github.com/coder/websocket`.
- **Live theme switching + user-editable themes** — themes are now **data, not
  code**: a `~/.hlab/themes.yaml` (seeded from an embedded default on first use,
  honoring `$HLAB_HOME`) defines each theme as a name plus ten semantic color roles
  (ANSI-256 or hex). Edit a color or add your own theme and it works without
  rebuilding hlab. File themes override/extend the three built-ins by name; the
  built-ins always remain as a fallback, so a deleted or broken file never breaks
  the binary, and any omitted color field falls back to the `default` palette's
  value. Switch themes three ways: the dashboard `t` key opens a selector that
  applies **live** (immediate re-render) and persists; a new `hlab theme` command
  lists the themes (marking the active one) and `hlab theme <name>` switches; or the
  existing `theme:` key in `config.yaml`. The `t` selector re-reads `themes.yaml`
  each time it opens, so custom colors appear without a restart.
- **Remembers the last VM username** — a successful `hlab vm create` now records the
  username it used (`default_user` in `~/.hlab/config.yaml`) and pre-fills it as the
  default for the next create (wizard, TUI create form and the `--user` flag). Falls
  back to your OS username when unset. LXC containers always log in as root, so they
  don't affect (or read) this default.
- **`hlab vm add-ssh-key` / `hlab ct add-ssh-key`** — add an SSH public key to an
  already-created guest. The key is installed on the live guest's
  `~/.ssh/authorized_keys` immediately over SSH (idempotent — cloud-init only
  injects keys at first boot) and recorded in the declaration. `--key` accepts a
  `.pub` path or a literal key, or prompts from `~/.ssh` when omitted. For VMs a
  targeted apply folds the key into Terraform state in place (guarded by a
  replace-veto so it can never recreate the VM); for containers Terraform ignores
  `initialization[0].user_account`, so no apply is needed and `hlab plan` stays
  clean.
- **Dashboard `i` — inject an SSH key** — with a managed guest selected, `i` opens
  a picker over the operator's keys (config + `~/.ssh`, deduped, default
  preselected) and, on confirm, installs the chosen key on the live guest and
  records it in the declaration as a streaming op — the TUI equivalent of
  `hlab {vm,ct} add-ssh-key`. The shared ssh-append helper moved to a new
  `internal/sshutil` package so the CLI and TUI use identical logic. Ignored for
  discovered (unmanaged) guests, like the other managed-only keys.
- **Configurable themes (M8)** — a `theme:` key in `~/.hlab/config.yaml` selects the
  color palette for the dashboard TUI and the CLI result boxes. Three built-ins:
  `default` (ANSI-256, respects the terminal's own scheme), `dracula` (truecolor) and
  `mono` (grayscale accent, keeping semantic good/warn/bad — an accessibility
  option). Every color is now a semantic role in a new `internal/theme` package
  rather than a raw literal; an unknown or empty name falls back to `default`. The
  active theme now also styles every CLI form — `hlab setup`, the create/provision
  wizard, `add-ssh-key` and confirmation prompts — not just the dashboard.
- **Checksum-verified install (M8)** — releases now publish a `SHA256SUMS` file, and
  `scripts/install.sh` verifies the downloaded binary against it, aborting on a
  mismatch (it warns and continues when `SHA256SUMS` or a local sha256 tool is
  absent — e.g. the first-release window). A README note covers the macOS Gatekeeper
  quarantine for manually-downloaded binaries (`xattr -d com.apple.quarantine`); the
  `curl | bash` path is unaffected.
- **`hlab doctor` git/ssh checks (M8)** — doctor now reports whether `git` (state
  versioning) and `ssh` (`hlab vm ssh` / provisioning) are on `PATH`, as soft checks.
- **`hlab setup --dotfiles-repo`** — set the dotfiles repository SSH URL
  non-interactively (also a field in the setup wizard / TUI setup form). Configuring
  it is what enables the new `dotfiles` provisioning option.
- **`--node` flag on `hlab vm create` and `hlab ct create`** — target a specific
  cluster node in the non-interactive (flags) create path. Falls back to
  `default_node` from config, then to the node holding the template.

### Changed
- **README rewritten for a public audience** — a short, scannable landing page
  (pitch + feature list + requirements + install + quick start + a compact command
  overview) with the deep reference material split into dedicated `docs/*.md` files:
  `docs/commands.md` (full command/flag reference + adopt), `docs/proxmox-token.md`
  (least-privilege token), `docs/lxc.md` (containers), `docs/ssh-keys.md`
  (add-ssh-key + keyless recovery), `docs/dotfiles.md`, and `docs/themes.md`. The
  README now links to these instead of repeating them. Added Apache 2.0 `LICENSE`,
  `NOTICE`, and `CONTRIBUTING.md`.
- **Create flow: mandatory password, no keyless warning, nesting always on (M8)** —
  a login password is now **required** when creating a VM or a container (interactive
  wizard, TUI create form, and the `--password` flag), guaranteeing a login method; an
  SSH key remains optional and additive. Because there is always a password, the
  "no SSH key" warning/confirm screen at create time is gone (the separate
  `add-ssh-key` / console-injection flow is unchanged). **LXC nesting is always on**
  for hlab-created containers (modern systemd guests misbehave without it, and it lets
  Docker/Podman run inside), so it is no longer a prompt/field and the `ct create
  --nesting` flag was removed — declarations still record `nesting: true`.
- **Dotfiles listed first (M8)** — when a `dotfiles_repo` is configured, the dotfiles
  entry appears **first** in the provision software checklist (both the CLI wizard and
  the TUI); it is still absent when no repo is configured.
- **Single home directory `~/.hlab` (M8)** — all of hlab's data (config.yaml,
  plans.yaml, `vms/`, `terraform/`, `ansible/`) now lives under one directory,
  overridable with the `HLAB_HOME` environment variable. `config.yaml` moved from the
  old `~/.config/hlab` into `~/.hlab` (still mode 0600, gitignored). hlab
  **auto-migrates** a pre-M8 `config.yaml` / `plans.yaml` from the legacy location on
  first run, printing a one-line stderr notice, and ensures `config.yaml` is
  gitignored before the move so the token can't be committed.
- **Dotfiles is now a software-catalog entry (M8)** — instead of a separate
  create/provision confirmation, `dotfiles` is a normal catalog key
  (`--software dotfiles`, or the checklist item "Dotfiles (terminal environment)").
  It only appears — and is only installable — when a `dotfiles_repo` is configured,
  and there is no built-in default repo. Existing declarations that recorded
  `dotfiles: true` are migrated into the software list on load. Private repos are
  still cloned over the forwarded SSH agent (a warning fires if the agent has no
  keys).
- **Generic defaults (M8)** — the default administrative username is now the current
  OS user (falling back to `admin`) instead of a hardcoded handle, and placeholders
  throughout use neutral values (`proxmox.example:8006`, `192.168.1.x`, `pve1`/`pve2`).

### Fixed
- **LXC nesting now defaults on** (matching the Proxmox web UI, which has since
  PVE 7.1) — an unprivileged modern-systemd container (e.g. Ubuntu 26.04) created
  without nesting has all its getty units crash-looping in lockstep (AppArmor blocks
  the remounts systemd needs), making console login impossible and flapping other
  services. Nesting is now **always on** for hlab-created containers (see the Changed
  section — it is no longer a prompt/field/flag). Nesting also lets Docker/Podman run
  inside, so provisioning `docker` no longer needs any nesting opt-in.
- **Console login no longer flakes on the first attempt** — while seeding a key into
  a keyless container over the console, our cursor-position replies (needed so agetty
  prints its login prompt) could be echoed as literal input into the username field
  once a prompt was already up, causing an intermittent "Login incorrect" on the
  first try. The console driver now stops answering size probes once a prompt
  appears, clears the input line (Ctrl-U) before typing the username and password,
  and retries the login once before giving up.
- **Keyless guests can't be reached over SSH until a key is injected** — hlab
  connects to a guest with **key auth only** (`AppendAuthorizedKey` uses
  `BatchMode=yes` and never prompts for a password), so a guest created with SSH
  key = **none** has no credential to authenticate with: an LXC container's root
  refuses SSH password auth by default (`PermitRootLogin prohibit-password`) and
  Ubuntu cloud VMs ship `PasswordAuthentication no`, so the login password works on
  the Proxmox console but not over SSH. That is exactly why the first key is seeded
  through an out-of-band channel — the Proxmox **console** for a container and the
  **QEMU guest agent** for a VM (see the two SSH-key injection entries above) —
  after which normal SSH works. An SSH **authentication** failure in the key-append
  helper is now translated into an actionable message instead of a raw ssh dump. (The
  password prompt users saw came from the *interactive* `hlab ct ssh` / dashboard `s`
  ssh session, not the inject path.) hlab itself renders **zero** keys for a keyless
  guest (regression-tested); note that a VM created with key = none that is still
  SSH-reachable has a key **baked into the golden image** — cloud-init merges the
  image's `authorized_keys` and hlab cannot strip it, so rebuild the template without
  the key if that matters.
- **Static-IP LXC containers had no network on Proxmox VE 9.1+** — on PVE 9.1+ a
  bpg container's `net0` defaults to `host-managed=0`, so Proxmox no longer writes
  the network config inside the guest; a container created with a **static IP** then
  booted with no address at all (DHCP containers were unaffected — the template's own
  networkd does DHCP). `hlab ct create` now queries the live Proxmox version and, on
  9.1+, sets `host_managed` on a static-IP container's interface so Proxmox owns the
  config. Older Proxmox never receives the parameter (rendered only when set), and
  `hlab ct adopt` carries an existing `host-managed=1` into the declaration so an
  already-fixed container doesn't drift.
- **Pre-M6 `plans.yaml` never gained its `lxc:` section (M8)** — an on-disk
  `plans.yaml` written before LXC support fell back to the embedded LXC plans on
  every read but was never repaired, so the LXC tiers stayed un-editable. `plans.Load`
  now appends the `lxc:` block (verbatim, preserving comments) to such a file once,
  idempotently.
- **Result box wrapped long labels** — the create/show summary box used a fixed
  11-column label, so the LXC-only "Unprivileged" label wrapped onto its own line
  ("Unprivilege / d"). The label column is now sized to the widest label present, so
  every label (VM and LXC) fits on one line.
- **Spinner flooded non-interactive output** — in quiet mode `runStep` always drew
  the animated huh spinner, so piping/capturing output (e.g. `hlab … 2>&1 | tail`)
  filled the stream with hundreds of braille frames and terminal control sequences.
  hlab now detects a non-TTY stdout and prints the step title once instead of
  animating; the ✓/error line and the returned result are identical to the spinner
  path. The dashboard TUI is unaffected (it renders its own progress; `runStep` is
  CLI-only).
- **`create` flags path ignored `default_node`** — non-interactive `hlab vm create`
  / `hlab ct create` derived the target node solely from where the template lived,
  taking the *first* node found during discovery. Since the same container template
  volume id (and, by name, a VM template) can exist on several nodes, this could
  silently place a guest on an unexpected node despite a configured `default_node`.
  Node selection now honors, in order, a new `--node` flag, then `default_node`, then
  the first node holding the template.

## [0.8.0] - 2026-07-02

### Added
- **Drift detection (M7)** — `hlab plan [name|id]` + TUI `P`: read-only whole-fleet
  `terraform plan` that reports only meaningful divergence (benign provider/
  bookkeeping noise filtered out); drifted guests marked in the dashboard.
- **Re-provision / update (M7)** — `hlab vm update` / `hlab ct update` (+ TUI `u`)
  re-run Ansible idempotently with the saved selection; `--upgrade` (TUI `U`) also
  upgrades OS packages (apt), mise runtimes, dotfiles, and self-updating CLI tools.
- **Adopt discovered guests (M7)** — `hlab vm adopt` / `hlab ct adopt` (and the TUI
  `a` key, on a **Discovered** guest) bring a VM or LXC container that already
  exists in Proxmox — but wasn't created by hlab — under hlab's management. It
  builds the declaration from the guest's live config (`internal/proxmox/
  guestconfig.go`), then imports it into Terraform state (`terraform import` +
  a post-import state patch for the config-only attributes the import leaves null)
  and verifies with a targeted plan that nothing would force a replace before
  declaring success. **The live guest is never modified or destroyed**: any
  failure — an unsupported shape, a failed import, or a plan that would force a
  replace — rolls the adoption back (state removed, declaration deleted, workspace
  resynced), leaving the guest exactly as found. Harmless in-place drift (e.g. the
  guest agent not yet enabled, or DHCP with no static IP set) is adopted anyway and
  reported as a warning, since the next apply reconciles it.
- The TUI title bar now shows the build version and commit (the same string
  `hlab version` prints), e.g. `hlab — homelab  v0.7.1-1a2b3c4`.
- **Cluster metrics panel** in the dashboard — a global fleet/cluster panel joined
  to the right of the per-guest detail panel: a compact fleet header
  (total guests · running), then each host node as a stacked block with **braille
  meters** (`⣿`) for CPU, RAM and its primary storage, plus the free GB alongside
  RAM/disk. Sourced from a single read-only `proxmox.ClusterMetrics` call (the
  unfiltered `/cluster/resources`, which also carries the node + storage rows
  `ClusterGuests` filters out); the fleet counts are derived from data already in
  the model. Refreshes on the 5s tick; hidden on terminals too narrow for the full
  tables. The detail panel's CPU/RAM gauges use the same braille meter.
- **Unit test suite** — first `*_test.go` coverage (`go test ./...`) for the pure,
  logic-heavy code with no live Proxmox/Terraform/Ansible dependency: plans
  (mem parsing / plan lookup), state store round-trips, config defaults +
  `SuggestIPCIDR`, the Terraform drift filter (`driftPaths`/`driftIgnore`) and
  adopt state helpers, Proxmox guest-config parsing, the software catalog, and the
  wizard/TUI/cmd formatting helpers.

### Changed
- The engine now depends on `Runner`/`Proxmox` **interfaces** (`internal/engine/
  deps.go`) instead of the concrete `*terraform.Runner`/`*proxmox.Client`, so the
  safety-critical create/adopt **rollback branches are unit-tested** with fakes
  (`rollback_test.go`) — asserting adopt never destroys the live guest, a vetoed
  adoption rolls back state + declaration, and a failed `state rm` surfaces as an
  incomplete rollback. Behavior is unchanged; the production types are the
  implementations (compile-time asserted).
- `hlab plan <name|id>` now runs a **targeted** `terraform plan` for just that guest
  instead of planning the whole fleet and filtering the output — materially faster
  on a large fleet. The no-arg `hlab plan` and TUI `P` still cover the whole fleet
  (and remain the only place an orphaned state resource is reported).
- Dashboard table columns now measure text in display cells (wide/CJK-aware) rather
  than rune count, so they stay aligned with non-ASCII names.
- The VM `clone` block (`main.tf`) and the container `operating_system` block
  (`container.tf`) are now conditional (`dynamic`, rendered only when
  `template_id`/`template_file` is set) with `lifecycle.ignore_changes` — containers
  also ignore `initialization[0].user_account` (entirely ForceNew and unreadable via
  the API). This removes the pre-existing footgun where editing `template_id` in a
  VM's YAML, or a container's ssh keys/password, could force a destroy-and-recreate
  on the next apply, and lets an adopted guest's first plan come back clean.
- `writeTfvars` now honors an explicit `MemoryMB` for VMs, matching the existing
  container behavior — needed for VMs adopted with RAM not aligned to a whole GB
  (e.g. 2560 MB). Resize now clears a stale `MemoryMB` when `MemoryGB` is set
  explicitly, so a later resize doesn't keep applying the old exact-MB value.

### Fixed
- **TUI title-bar version no longer escapes the table's right edge** when a managed
  guest is selected. The version was right-aligned to `max(tables, footer)` width,
  and a managed VM's footer keybinding line is wider than the tables — so the
  version got pushed past the table. It now aligns to the table width only (the
  footer can still be wider; the block is centered by its true widest line).
- **TUI managed table is navigable from the first frame.** On a fresh first launch
  the initial refresh waits on the (cold) Proxmox API, so the table was empty for a
  few seconds and `↑/↓` appeared dead. The managed rows are now seeded from local
  state (a fast YAML read) at construction, so they're present and selectable
  immediately; live status / discovered guests still fill in from the async refresh.
- **`create` now rejects a name that's already managed**, closing a data-loss
  footgun found in review: previously only the VM ID was checked, so `create`
  with an existing hostname (but a new ID) overwrote the declaration and the next
  apply planned a destroy-and-recreate of the live guest — and `create --dry-run`
  on an existing name deleted that guest's declaration outright. Both `Create` and
  `DryRun` now refuse a colliding name up front (fail-closed even if the existing
  declaration is unparseable).
- **Terraform state-patch / drift-plan temp files no longer risk leaking a secret
  into git.** `PatchResourceAttrs` and `DriftReport` wrote `.state-patch.json` (a
  `state pull` dump containing the cloud-init password) and `.drift.tfplan` under
  the git-tracked `~/.hlab/terraform`; a hard-kill before the deferred cleanup
  could leave them for the next `git add -A`. They now go to `os.CreateTemp` (0600,
  outside the repo).
- **TUI could hang on a single output line larger than 1 MB.** `pipeInto`'s scanner
  capped lines at 1 MB; a longer line ended the reader early and blocked the
  terraform/ansible writer forever. It now drains the pipe after the scan loop.
- **Data race between the Setup form and the dashboard refresh.** Reloading the
  engine after `setup` mutated shared `Runner`/`PM` fields while the periodic
  `refresh` / IP-resolve workers read them. `refresh`/`resolveGuestIPs` now
  snapshot those on the UI goroutine before spawning, and the reload swaps fresh
  `Runner`/`PM` pointers instead of mutating in place.
- Snapshot names are URL-escaped in the Proxmox rollback/delete paths, and the TUI
  edit form preserves a VM's non-GB memory size (e.g. 2560 MB no longer silently
  narrows to 2048 MB when re-applied unchanged).
- The dashboard tables now paint immediately on launch: per-guest IP lookups
  (host interfaces for LXC, the guest agent for VMs) moved out of the initial
  refresh into a concurrent follow-up that fills the IP column when ready. A
  single running VM without the QEMU guest agent used to block the whole first
  load for seconds (the agent API call hangs before failing), multiplied by
  every running discovered guest resolved serially.
- A stale comment on `migrateContainer` claimed container migration re-anchors the
  Terraform resource with `state rm` + `import`; it actually patches `node_name` in
  place (`Runner.SetResourceNode`), which is what keeps a migrated container
  manageable. Comment corrected to match the code.
- **Failed adopt rollback could leave an invisible orphan in Terraform state.**
  If `terraform state rm` failed while rolling back a partial `adopt`, the
  resource stayed in state while its declaration was deleted — an orphan a later
  untargeted apply would plan to destroy — yet `Adopt` still reported a clean
  "rolled back, guest untouched", and `hlab plan`/TUI drift detection silently
  dropped it (they only classified *declared* guests). Now the `state rm` error
  is surfaced with recovery instructions, and `DetectDrift` reports any state
  resource with no matching declaration as a new `orphaned` state so `hlab plan`
  and the TUI stay a trustworthy audit of the fleet. (The live guest was never at
  risk — hlab runs no untargeted apply — but the audit gap is closed.)

## [0.7.1] - 2026-07-01

### Fixed
- **Migrating an LXC container left it un-manageable** — after `ct migrate`, a
  later `ct destroy` / `ct resize` failed instantly with `context deadline
  exceeded`. The migration re-anchored the Terraform resource with `state rm` +
  `import`, and an imported bpg container comes back with its config-only
  attributes null in state (notably `vm_id` and the `timeout_*` fields). The
  provider derives the delete/update operation's context deadline from
  `timeout_delete`/`timeout_update`, so a null there produced a zero-length
  deadline and the next status read failed immediately — leaving the container
  impossible to update or destroy via Terraform. Migration now patches only
  `node_name` directly in state (state pull → edit → push), preserving the
  create-time attributes, so a migrated container stays fully manageable.
- Failed Proxmox tasks already surface the task-log tail; combined with the above,
  container migrate/destroy now behave correctly.

## [0.7.0] - 2026-07-01

### Added
- **LXC container management (M6, v1)** — hlab now creates and manages LXC
  containers alongside VMs, not just powers them as discovered guests.
  - New **`hlab ct`** command group: `create · list · show · start · stop · reboot ·
    destroy · provision · ssh`. Containers are created from a container template
    (vztmpl volume id) via the bpg `proxmox_virtual_environment_container` resource
    (`assets/terraform/container.tf`), with `unprivileged` (default on), `nesting`
    (for Docker-in-LXC) and optional swap.
  - **LXC plans** — `micro` (1c/512MB/4GB, default) · `small` (1c/1GB/8GB) ·
    `medium` (2c/2GB/16GB) · `large` (4c/4GB/32GB), under a new `lxc:` section in
    `~/.config/hlab/plans.yaml` (seeded from the embedded default; existing installs
    fall back to the embedded LXC plans).
  - **TUI create flow** gained a first screen to choose **VM vs LXC**; the LXC path
    offers container templates, LXC plans, and unprivileged/nesting/swap options.
  - **Provisioning** works over the container's SSH (as root); the Ansible playbook
    skips the cloud-init wait on minimal templates that lack it and installs a
    service-ready base package set (git/curl/sudo/…) on containers first, so
    dotfiles and `curl|bash` installers work on a stock (minimal) LXC template.
  - Container IPs are discovered from the host (LXC `interfaces` API), so even a
    DHCP container can be provisioned/ssh'd without a guest agent.
  - **Networking**: static IP is recommended (containers have no guest agent, so a
    DHCP address can't be auto-discovered); DHCP is allowed but its IP isn't
    auto-resolved.
  - **LXC snapshots** — `hlab ct snapshot|snapshots|rollback|snapshot-delete` and
    the TUI snapshot browser (`v`) now work for containers (via the `/lxc/`
    snapshot API; no RAM/live-state option, since containers have none). A rollback
    restarts the container if it was running (snapshots without RAM leave it stopped
    otherwise).
  - **LXC resize** — `hlab ct resize` and the TUI edit form (`e`) change a
    container's cores / RAM / disk in place (disk grows only).
  - **LXC migration** — `hlab ct migrate --to <node>` (and the TUI `m` key) move a
    container between nodes. The bpg container resource has no `migrate` attribute,
    so hlab migrates via the Proxmox API (a running container is restarted) and then
    re-anchors the Terraform resource to the new node (state rm + import) so state
    stays consistent without a destroy/recreate. A pre-flight check refuses (with a
    clear message) to migrate a container that has snapshots on non-shared storage,
    which Proxmox can't migrate — before it would shut the container down and abort.
- Failed Proxmox tasks now surface the tail of the task log (not just the terse
  exit status like "migration aborted"), so the real cause is visible.
  - **Consistent memory units** — container memory is entered in **GB by default**
    (like VMs): a bare number is GB (`2`), and the sub-GB case takes an explicit
    suffix (`512M`, or `0.5`). Applies to `ct create`/`ct resize`, the wizard and
    the TUI create/edit forms.
- Discriminator + plumbing: `VMSpec.Type` (`vm`/`lxc`, back-compatible — existing
  declarations without `type:` stay VMs), type-aware Terraform targeting, and
  `proxmox.ContainerTemplates`/`ContainerStorages` discovery.

## [0.6.1] - 2026-06-30

### Changed
- **Create wizard order** — reordered for a more intuitive flow that matches how you
  think ("what to run" before "what machine"): **template (image)** first, then
  **plan (size)**, then VM ID / hostname / networking, then user & review. The
  conditional detail screens now sit next to what triggers them — the custom
  CPU/RAM/disk inputs come right after the plan (only for "Custom"), and the static
  IP/gateway/DNS inputs right after the networking choice (only for "Static").
  Applies to both the CLI wizard and the TUI create form.

## [0.6.0] - 2026-06-30

### Changed
- **Slimmer dashboard footer**: the keybinding bar now shows only the common
  actions; `d` destroy, `m` migrate, `S` setup and `R` refresh still work but are
  documented in the `?` window (renamed from "help" to **keybindings**), keeping the
  footer readable as the action set grows.

### Added
- **Reconfigure / resize an existing VM** (M5) — change an existing VM's CPU / RAM /
  disk (disk grows only). `hlab vm resize <name|id>` with `--cores` / `--memory` /
  `--disk` / `--plan`; in the dashboard, press `e` on a managed VM for an edit form
  (plan or custom, pre-filled from the current spec). The node stays put and the VM
  keeps its disk/id — it edits the declaration and re-applies Terraform, which
  updates the VM in place (no destroy/recreate); on failure the declaration is
  restored. CPU/RAM changes may need a reboot to take effect; growing the filesystem
  to fill a larger disk happens on reboot (cloud-init growpart) or manually.
- **`hlab vm migrate <name|id> --to <node>`** — move a managed VM to another
  cluster node, preserving its disk and VM id (M5). The node is part of the
  declaration, so the move goes through Terraform: the VM resource now sets
  `migrate = true`, so the `bpg/proxmox` provider migrates the guest (online when it
  is running) instead of the default destroy-and-recreate. `migrate` updates the
  declaration, applies, and commits; on failure the declaration is restored so
  state/tfvars/reality stay in agreement. Note: with node-local storage the target
  node must have the same storage available.
- **Dashboard TUI: migrate a VM** — press `m` on a managed VM to pick a target node
  (a modal form) and stream the migration in the progress window, mirroring the
  destroy flow. Added to the `?` help overlay and the footer.
- **Live utilization in the dashboard** (M5) — the detail panel now shows real-time
  CPU and RAM gauges (used, not just allocated) plus uptime for the selected VM or
  discovered guest, colored green/yellow/red by load. The figures come from the
  existing cluster-wide `cluster/resources` call (it already reports `cpu`/`mem`/
  `uptime`), so there is no extra API request — they refresh with the 5s tick.
- **Snapshots** (M5) — a safety net before risky changes. New CLI verbs
  `hlab vm snapshot <name|id> <snap>` (with `--description` / `--ram`),
  `hlab vm snapshots <name|id>`, `hlab vm rollback <name|id> <snap>` and
  `hlab vm snapshot-delete <name|id> <snap>` (rollback/delete confirm unless
  `--yes`). In the dashboard, press `v` on a managed VM to open a snapshots browser
  (create with `c`, roll back with `r`, delete with `d`). Snapshots are runtime
  state (via the Proxmox task API; the engine waits for the task and surfaces
  failures), so nothing is persisted to the declaration. Snapshots require the
  `VM.Snapshot` / `VM.Snapshot.Rollback` privileges on the API token role (grant
  with `pveum role modify HLab --privs "VM.Snapshot,VM.Snapshot.Rollback" --append 1`).

### Fixed
- **Dashboard TUI hid operation errors**: a failed create/provision/destroy/migrate/
  snapshot op set the error and then immediately refreshed, and the successful
  refresh cleared it (~200 ms), so failures looked like "nothing happened". Operation
  errors now live in a separate field that the refresh does not wipe; they stay in
  the footer until the next operation or a manual refresh (`R`).

## [0.5.1] - 2026-06-30

### Changed
- **The VM creation flow no longer asks for a node** — the chosen template
  determines it. VM ids are cluster-unique and, with node-local storage
  (`local-lvm`), a clone must land on the template's node, so a separate node
  prompt only offered impossible combinations. The TUI form and the CLI wizard now
  show a single template list spanning all nodes (labelled with each template's
  node); the `hlab vm create --node` flag is removed; and `--template` defaults to
  the configured default template. To run a VM on another node, build a template on
  that node and pick it.
- **`hlab setup` clarity**: the node selector is relabelled "Discovery node" with a
  note that it only scopes storage/bridge discovery (not where VMs run), and the
  "Default template" list now spans all nodes (via `proxmox.Client.AllTemplates`)
  instead of just the discovery node.

### Fixed
- **Create form template list hid options / flickered**: the TUI create wizard used
  a dynamic `OptionsFunc` for the Template select, which (a) called the Proxmox API
  on the UI loop while navigating and (b) in huh v1.0.0 forces a fixed height that
  pins the selected row to the top of the viewport, hiding the options above it — so
  moving between templates made the other one disappear, and a node with no
  templates left an empty list. The Node + dynamic-Template selects
  are replaced by a single static Template list spanning all nodes (labelled with
  each template's node); the chosen template determines the node the VM lands on
  (VM ids are cluster-unique). Options are fetched once when the form opens.
- **Confusing failure when the VM ID is taken**: creating a VM with an ID already
  used by another guest failed deep inside `terraform apply` ("`<vmid>.conf`
  failed: File exists") and then rolled back, which in the TUI looked like the
  progress bar cutting off. `Create` now rejects a clashing VM ID up front with a
  clear message naming the conflicting guest, and the TUI create form validates the
  VM ID field against the cluster's in-use IDs.

## [0.5.0] - 2026-06-30

### Added
- **`nano` plan** (1 core / 1 GB / 10 GB): the minimum VM footprint, for tests,
  a bastion/jump host, or a single-purpose lightweight service that needs VM (not
  LXC) isolation. Not intended for the full provisioning profile — use KVM1+ for a
  real provisioned server.

### Changed
- **Plans now scale disk per tier** instead of a flat 32 GB: nano 10 GB, KVM1
  16 GB, KVM2 32 GB, KVM4 48 GB, KVM8 64 GB. Storage is thin-provisioned, so the
  larger virtual disks only consume the blocks actually written. Assumes the
  golden image is rebuilt with a 10 GB disk (the new minimum floor); a clone can
  only grow, never shrink, below the template, and cloud-init/growpart expands the
  filesystem on first boot.
- The manual `--disk` default dropped from 32 to 16 GB, and the wizard's fallback
  disk (when the template size cannot be read) from 32 to 10 GB, to match the
  smaller floor. The live floor is still read from the template and enforced.

### Fixed
- **VM memory reads ~100% in the Proxmox UI until a reboot**: clones now enable
  the virtio-balloon device (fixed — floating equals the assigned memory, so the
  VM's RAM is never reduced). Without it, Proxmox displays the QEMU process RSS,
  which the guest page cache inflates to near 100% after provisioning. The balloon
  driver reports the real (cache-excluded) usage from first boot. Existing VMs pick
  this up when recreated, or via `qm set <vmid> --balloon <memory_mb>`.

## [0.4.0] - 2026-06-30

### Added
- **Power control**: start, stop and reboot VMs without leaving hlab. New CLI
  commands `hlab vm start <name|id>`, `hlab vm stop <name|id>` (a graceful guest
  shutdown by default, `--force` for a hard stop) and `hlab vm reboot <name|id>`
  (graceful guest reboot). In the dashboard TUI, `b` toggles power on the
  selected VM — starting it when stopped, gracefully shutting it down when
  running — and `r` reboots it; the STATUS column reflects the new state on the
  next refresh. Power is a runtime action handled through the Proxmox API, not
  Terraform, so the VM declaration is untouched.
- The dashboard's manual refresh moved from `r` to `R` (now that `r` reboots);
  the table still auto-refreshes every 5s, so `R` is only for an immediate one.
- **Discovered (unmanaged) resources** in the dashboard: a second "Discovered"
  table lists every VM and LXC container that exists in Proxmox but was not
  created by hlab. Selection flows continuously across both sections with `↑/↓`.
  Discovered guests are power-only — `b` (start/stop) and `r` (reboot), each
  behind a yes/no confirmation; provision/destroy/ssh are hidden for them and the
  footer adapts to the selection. They are never provisioned or destroyed (no
  declaration exists). Backed by a single cluster-wide query
  (`/cluster/resources`) that also now drives the managed STATUS column, replacing
  the per-VM status calls. The two sections are sized to the terminal height —
  Managed stays pinned at the top while a long Discovered list scrolls around the
  cursor with `↑/↓ N more` indicators.

### Changed
- The dashboard is now centered instead of hugging the left edge of wide
  terminals, and the detail panel and keybinding footer are pinned to the bottom
  so they no longer move while navigating.
- The dashboard tables are now rendered directly (no bubbles `table` widget),
  enabling the two stacked sections and a single cursor that spans both.

## [0.3.0] - 2026-06-29

### Added
- **Preconfigured VM plans** (KVM1/KVM2/KVM4/KVM8 by default): pick a t-shirt size
  instead of typing cores/memory/disk. Plans live in a user-editable
  `~/.config/hlab/plans.yaml` (seeded on first use from an embedded default), so
  sizes can be changed without rebuilding. Offered in the TUI create form and the
  CLI wizard (default selection KVM2), with `--plan KVM4` for scripting and a
  "Custom" option for manual specs. The chosen plan is recorded on the VM and
  shown in the detail/show views. New package `internal/plans`.

### Changed
- Storage is no longer asked when creating a VM (TUI form, CLI wizard, and the
  `--storage` flag removed). Like the default network bridge, it is configured
  once in `hlab setup` and reused for every VM.

### Fixed
- The TUI create form defaulted the disk to 20 GB, which could fail against the
  32 GB golden image (a clone cannot shrink the template). The default is now
  32 GB; the bump-to-template safeguard remains.

## [0.2.0] - 2026-06-29

### Added
- **Dashboard TUI (M3)**: running `hlab` with no subcommand launches a
  full-screen terminal UI — a table of managed VMs with a detail panel. VM
  management happens inside the app: create (`n`), provision (`p`), ssh (`s`),
  destroy (`d`) and setup (`S`). The create/provision/destroy/setup wizards are
  embedded forms rendered as a centered modal floating over the dashboard; only
  ssh suspends to an external session. Long operations (Terraform/Ansible) stream
  into a fixed-size progress window with an animated bar; `l` toggles a dark,
  ANSI-stripped output box. Built on bubbletea/bubbles/huh/lipgloss.
- Dashboard **VM power status** column (running/stopped), read from Proxmox via a
  new read-only `proxmox.VMStatus`, plus a **periodic refresh** (every 5s) that
  updates the table while on the dashboard.
- **Help overlay** (`?`) listing every keybinding, and a **scrollable output
  box** during runs (`↑/↓`/PgUp/PgDn; auto-follows the newest line unless you
  scroll up) backed by a fixed-size viewport.
- **Cancel a running operation** with `esc`/`ctrl+c` in the TUI: the Terraform/
  Ansible process is bound to a context and killed; a cancelled create still
  rolls back its partial VM. The runners gained an optional `Ctx`.
- **Review step** in the create wizard: a summary of the chosen specs is shown
  before the final confirm.

### Fixed
- Modal text (progress bar, help, hints, status) no longer renders on islands of
  the terminal's default background ("selected text" look); every in-modal
  element now carries the modal background.

### Changed
- Orchestration (create with rollback, provision, destroy, IP resolution) moved
  into a new presentation-free `internal/engine`, shared by the CLI and the TUI
  so the logic lives in one place. No change to CLI behavior.
- The Terraform and Ansible runners gained an optional `Out` writer used to
  stream output into the TUI; unset, they keep the CLI capture/`-v` behavior.

## [0.1.3] - 2026-06-27

### Added
- Installer `scripts/install.sh` (idempotent, re-run to update) that builds from a
  local checkout, or downloads the release binary / `go install` from a URL. Plus
  a GitHub Actions release workflow that publishes cross-compiled binaries on tag,
  and `mise run install` / `mise run build` tasks.
- `hlab vm show <name|id>` shows a VM's details and what was provisioned
  (software + dotfiles). `hlab vm list` now lists the provisioned software per VM
  instead of an item count.
- Additional-software catalog: **Claude Code**, **Open Code** and **Hermes agent**
  CLIs (installed via their official `curl | bash` installers, user-local). Hermes
  installs headless (`--skip-setup --skip-browser --non-interactive`); configure it
  later with `hermes`.

## [0.1.2] - 2026-06-27

### Fixed
- Provisioning now waits for cloud-init and the apt/dpkg lock before installing,
  avoiding transient first-boot failures (cloud-init / unattended-upgrades holding
  the lock made apt and the Docker script error out). apt tasks also use a
  `lock_timeout`.

### Changed
- Software and dotfiles selection moved from `vm create` to `vm provision`, where
  it is actually installed (the `--software` / `--dotfiles` flags moved too). The
  selection is persisted to the VM declaration. `vm create` now covers only the
  VM lifecycle; provisioning is always a separate step.
- Provisioning options are shown in two screens: dotfiles first, then software.

## [0.1.1] - 2026-06-27

### Fixed
- After creating a static-IP VM, verify the address actually applied and, if the
  guest is still on a DHCP lease (Ubuntu renames the NIC to `eth0` only after a
  reboot), reboot it once and wait for the static IP. This also avoids the slow
  first boot caused by `systemd-networkd-wait-online` waiting for `eth0`.
- For VMs with a static IP, report and use the **declared address** instead of the
  guest-agent reading. A VM could briefly hold a transient DHCP lease during first
  boot before cloud-init applied the static config, making `vm list` and the result
  screen show the wrong (DHCP) address even though the VM ended up on its static IP.
  `vm list`, the result screen, `vm provision` and `vm ssh` now trust the static
  declaration.

### Added
- `hlab setup` now captures a **default template** (flag `--template`); the wizard
  preselects it so you don't pick the template every time.
- `hlab setup` now captures a default **gateway** and **subnet prefix (CIDR)**
  (flags `--gateway` / `--cidr` for non-interactive setup).
- `vm create` pre-fills a **suggested static IP** — the next free address starting
  at `.10` in the gateway's subnet — along with the gateway, and defaults to
  static addressing when a gateway is configured.
- Animated progress spinner during `vm create`, `vm provision` and `vm destroy`
  in quiet mode, so long operations show activity without `-v`.

### Changed
- The post-create result screen now suggests the next commands using the VM ID
  (`hlab vm ssh <id>`, and `hlab vm provision <id>` when software/dotfiles are
  selected).

## [0.1.0] - 2026-06-26

Initial release. hlab creates and provisions Proxmox VMs by discovering the
infrastructure via the Proxmox API and orchestrating Terraform and Ansible — the
goal being to think *"I want a new server"*, not *"I want to configure Terraform"*.

### Added

- **Interactive wizard** (`hlab vm create`) that discovers Proxmox nodes,
  templates, storages and bridges from the API and asks only for what it cannot
  infer. Every field also has a flag for non-interactive / scripted use.
- **Global setup** (`hlab setup`) storing connection details and defaults in
  `~/.config/hlab/config.yaml`; scans `~/.ssh` to offer keys. `--add-node` and
  `--add-ssh-key` extend an existing configuration.
- **Health check** (`hlab doctor`) for Terraform, Ansible and Proxmox connectivity.
- **VM lifecycle via Terraform** using the `bpg/proxmox` provider: clones the
  Ubuntu Golden Image template and applies cloud-init (DHCP or static IP, user,
  SSH key, optional password). Follows the homelab standards (q35, SeaBIOS,
  `x86-64-v2-AES`, VirtIO SCSI, QEMU guest agent).
- **Provisioning via Ansible** (`hlab vm provision`): Docker, Podman, K3s and
  language runtimes (Node.js, Go, Python, Rust) installed through mise; optional
  dotfiles installed by cloning the repo and running its `bootstrap.sh` (server
  profile). Private dotfiles repos are cloned using a forwarded SSH agent, so the
  private key never leaves the operator's machine.
- **VM management**: `hlab vm list` (with discovered IPs), `hlab vm destroy`
  (`--yes` to skip confirmation) and `hlab vm ssh` (interactive session).
- **Name-or-ID** resolution: VM subcommands accept a VM name or its numeric ID.
- **Verbosity control** (`-v`, `-vv`): quiet by default — Terraform/Ansible output
  is hidden and shown only on failure; `-v` streams it live.
- **Result screen** after creation summarizing the VM and its IP address.
- **Versioned state** (homelab-state at `~/.hlab`): each VM is a declarative YAML,
  auto-committed on create/destroy. Secrets and Terraform state are gitignored.
- **`hlab version`** reporting the version and the short build commit
  (for example `hlab v0.1.0-1a2b3c4`).
- **Self-contained binary**: the Terraform workspace, Ansible content and software
  catalog are embedded via `//go:embed`.

### Security

- Proxmox authentication uses a least-privilege API token; the secret is stored
  locally (`~/.config/hlab/config.yaml`, mode `0600`) and never committed.
- Per-VM cloud-init passwords are written to a gitignored Terraform secrets file,
  never to the versioned declarations.
- Private dotfiles repositories are cloned via SSH agent forwarding rather than
  copying a private key onto the VM.

### Fixed

- Default and enforce the VM disk size to be at least the template's disk size,
  preventing the Terraform clone from failing with "requested size lower than
  current size".
- Roll back a partially-created VM (and drop its declaration) when creation fails,
  so retries are not blocked by a leftover ("config file already exists").
