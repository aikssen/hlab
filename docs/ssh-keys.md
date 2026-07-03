# SSH keys & keyless recovery

hlab connects to guests with **key auth only**. This doc covers adding keys to a
running guest and how hlab recovers a guest that has no key yet.

## Why a password is required at create time

**A password is required at create time** (VMs and containers), so there is always
a login method — an SSH key is optional and additive. Even so, **prefer also
picking an SSH key**, especially for containers.

## `hlab {vm,ct} add-ssh-key`

Adds an SSH public key to an existing guest: it installs the key on the live
guest's `authorized_keys` immediately and records it in the declaration. `--key`
takes a `.pub` path or a literal key (hlab prompts from `~/.ssh` when omitted).
Available in the TUI as the `i` key.

When the guest is already reachable by SSH, hlab appends the key over SSH. When the
guest is **keyless** (no key hlab can use yet), it falls back to an out-of-band
injection path — see below.

## Keyless containers (console injection)

A container's root refuses password auth over SSH by default
(`PermitRootLogin prohibit-password`), so a container created with **no SSH key**
is initially **console-only**: the root password works in the Proxmox web console
but **not over SSH**, and hlab (which connects with key auth only) can't reach it —
`provision`, `update` and `ct ssh` won't work until a key is added.

This is **recoverable**. `hlab ct add-ssh-key` (or `i` in the TUI) injects the
first key **over the Proxmox console**: hlab drives the same termproxy websocket the
web console uses, logs in with the container's root password, and appends the key to
`/root/.ssh/authorized_keys`. It uses the root password stored at create time (in
the machine-local, gitignored secrets file); **when that isn't available** — a
container created on another machine or by an older hlab — hlab **prompts** for the
root password (or you can pass `--password`). A non-interactive run with no password
errors instead of hanging.

Requires the **`VM.Console`** privilege on the API token (see
[proxmox-token.md](proxmox-token.md)); without it the call fails with `403 …
VM.Console`.

## Keyless VMs (guest-agent injection)

A **keyless VM** is locked out the same way: Ubuntu cloud images ship
`PasswordAuthentication no`, so sshd refuses the cloud-init password and hlab
connects with key auth only.

It is also **recoverable**. `hlab vm add-ssh-key` (or `i` in the TUI) injects the
first key through the **QEMU guest agent**, which runs as root inside the VM and
needs no login (the VM analogue of the container console path — a VM's graphical
console is VNC, not a text stream). The key is fed on stdin (`input-data`), so it is
never shell-quoted.

Requires `qemu-guest-agent` running in the VM (hlab's golden images set `agent=1`)
and the **`VM.GuestAgent.Unrestricted`** privilege on the API token (see
[proxmox-token.md](proxmox-token.md)); a missing privilege surfaces `403 …
VM.GuestAgent.Unrestricted` with the fix.

## Baked-in template keys

A VM cloned from a golden image that has a key **baked into its `authorized_keys`**
stays SSH-reachable even when created with key = none: hlab renders zero keys itself,
but cloud-init merges the image's existing keys and hlab can't remove them. Rebuild
the template without the key if that matters.
