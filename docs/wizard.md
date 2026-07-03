HLab VM Creation Wizard

I would like to redesign the VM creation wizard to provide a much better user experience. The objective is that the user thinks in terms of “I want a new server”, not “I want to configure Terraform”.

The wizard should only ask for information that cannot be inferred automatically. Everything else should come from configuration defaults or from the Proxmox API.

Global Configuration (hlab setup)

The first time HLab is executed, it should provide a setup command (hlab setup) to configure global settings.

These settings should be stored in a configuration file and reused automatically by future commands.

Suggested configuration:

* Proxmox URL
* API Token ID
* API Token Secret
* Default Proxmox Node 
* Default Storage (where to create the vm, offer local-lvm as default)
* Default Network Bridge (offer vmbr0 as default)
* Default SSH Public Key (scan local .ssh folder to offer keys)(Explain in a short text why this is needed)

Allow to add more nodes and ssh keys later on with somehting like `hlab setup --add-node xxx` and `hlab setup --add-ssh-key`

The wizard should never ask for these values again unless the user explicitly changes them.

⸻

Automatically Discovered Information

HLab should use the Proxmox API to discover infrastructure instead of asking the user.

Examples:

* Available Nodes [pve1|pve2]
* Available Templates
* Available Storages
* Available Network Bridges

These should be presented as selectable options instead of requiring manual input.

⸻

Wizard Flow

The wizard should follow a logical order.

Step 0 - Guest type (VM or LXC)

The dashboard's "new" flow opens by asking whether to create a **virtual machine**
or an **LXC container**. The choice drives the rest of the flow: which template
source, plan catalog and options are shown. From the CLI the choice is implicit —
`hlab vm create` is a VM and `hlab ct create` is a container — so this screen only
appears in the TUI.

For an LXC container the flow mirrors the steps below, with a few substitutions:

* **Template** — a *container template* (a `vztmpl` volume already present on a
  Proxmox storage, e.g. `local:vztmpl/debian-12-standard_…tar.zst`), not a VM
  template. It still determines the node.
* **Plan** — the LXC catalog (`micro`/`small`/`medium`/`large`, default `micro`).
* **Memory** — GB by default (like VMs); the sub-GB case takes an explicit suffix
  (`512M`, or `0.5`).
* **Container options** — an extra screen for `unprivileged` (default on) and
  optional swap. `nesting` is **always on** for hlab-created containers (modern
  systemd guests misbehave without it, and it lets Docker/Podman run inside), so it
  is not a prompt or a flag.
* **User** — a container logs in as **root** (no separate admin user).
* **Networking** — static is recommended (containers have no guest agent, so a DHCP
  address can't be auto-discovered — though hlab still reads a running container's
  IP from the host).

Everything else (VM ID/hostname, static addressing, SSH key, review) is the same.

Step 1 - Template (image)

-Select Template.

The image comes first: you decide *what* to run before *what size* it needs. The
list is obtained dynamically from Proxmox and shows every node's VM templates,
labelled with the node each one lives on. Only VM Templates are shown.

Example:

* ubuntu-24.04-template (#200) · pve1
* debian-12-template (#100) · pve2

There is no separate "node" question. The chosen template determines the node the
VM is created on: VM ids are cluster-unique and, with node-local storage
(local-lvm), a clone must land on the template's node. To run a VM on another
node, build a template on that node — it then appears in this list.

Storage is not asked here either; it is configured once in `hlab setup` and reused
for every VM.

⸻

Step 2 - Plan (size)

-Select a Plan.

Pick a preconfigured size (KVM1/2/4/8) or "Custom (enter specs)". A preconfigured
plan fills in CPU/RAM/disk; Custom asks for them next (Step 3).

⸻

Step 3 - Custom specs (only for a Custom plan)

Shown right after the plan when Custom was chosen, so sizing stays together:

* CPU cores (only the number of cores).
* Memory (GB) — internally converted to MB for Terraform.
* Disk (GB) — default and minimum is the template's disk size (a clone cannot
  shrink it).

A preconfigured plan skips this screen entirely.

⸻

Step 4 - Identity & networking

-Enter VM ID.

I intentionally organize VM IDs by ranges, so I want to choose the VM ID manually.

-Enter Hostname Name.

This should become:

* Proxmox VM Name
* Hostname
* Terraform Resource Name
* Inventory Name

-Networking.

First ask:

* DHCP
* Static

⸻

Step 5 - Static addressing (only when Static)

Shown right after networking when Static was chosen:

* IP Address (allow to add CIDR here)
* Gateway
* DNS Servers (optional)

DHCP skips this screen, so no unnecessary questions are asked.

⸻

Step 6 - User creation

-Administrative Username.

Defaults to your OS username (falling back to `admin`). Example:

admin


-User password

A login password is **required** (both VMs and containers), guaranteeing a working
login method. An SSH key is optional and additive on top of it.


-Use SSH (optional*)

list the available configured SSH keys.

Example:

* none
* laptop
* yubikey
* custom


⸻

Note — provisioning options moved

Dotfiles and additional software are NOT chosen during creation anymore. They are
selected (and installed) in the provisioning phase: `hlab vm provision`. This keeps
the selection where the installation actually happens. See "Provisioning phase" below.

⸻

Provisioning phase (`hlab vm provision`)

Show a single checklist of additional software installed using Ansible. There is no
longer a separate dotfiles confirmation: **dotfiles is a catalog entry like any
other** (`Dotfiles (terminal environment)`), which appears **first** in the list
(when a `dotfiles_repo` is configured, `hlab setup --dotfiles-repo …`) — selecting it
clones that repo and runs its `bootstrap.sh`. The prompts default to whatever the VM
declaration already records (so re-provisioning remembers the previous choice), and
the selection is persisted back to the declaration.

The catalog lives in `assets/additional-software.yaml` (embedded). If no runtime
brings in mise (and dotfiles isn't selected either), Ansible installs mise itself.


──────────────────────────────────────
 Additional Software
──────────────────────────────────────

[ ] Dotfiles (terminal environment)   ← listed first, only when dotfiles_repo is set
[ ] Docker Engine
[ ] Podman
[ ] K3s
[ ] Node.js (mise)
[ ] Go (mise)
[ ] Python (mise)
[ ] Rust (mise)

[ Continue ]

Non-interactive: `hlab vm provision <name|id> --software docker,node,dotfiles`.

⸻

Step 7

Review Screen.

Display a complete summary before provisioning.

Example:

Node: pve1
Template: ubuntu-24.04-template
Storage: local-lvm
VM ID: 6100
VM Name: ubuntu-sandbox
CPU: 4
RAM: 8 GB
Disk: 20 GB
Network: DHCP
User: admin
Login: password|ssh key

Then ask for confirmation.

⸻

Step 8

Provisioning.

After confirmation, HLab should execute Terraform to create the VM.

Future versions will optionally execute Ansible after Terraform completes.



⸻

Design Principles

The wizard should follow these principles:

* Never ask for information that can be discovered automatically.
* Never ask for information that can be inferred.
* Never ask for information that already has a configured default.
* Always provide sensible defaults.
* Keep the number of questions as small as possible.
* Present information in a logical order.
* Focus on creating a server, not configuring Terraform.

The goal is for HLab to feel like a platform management tool rather than a wrapper around Terraform and Ansible.







