# LXC containers

hlab manages LXC containers alongside VMs through the `hlab ct` command group (and
the VM-vs-LXC choice in the TUI create flow). Everything below is container-specific;
for the general command reference see [commands.md](commands.md).

## Templates (vztmpl)

Containers are created from a **container template** — a `vztmpl` volume already
present on a Proxmox storage, e.g. `local:vztmpl/debian-12-standard_…tar.zst`.
Download one in the Proxmox web UI first (Storage → CT Templates → Templates),
then pass it with `--template-file` (or pick it in the wizard):

```bash
hlab ct create --name web --vmid 6101 \
  --template-file 'local:vztmpl/debian-12-standard_…tar.zst' \
  --dhcp=false --ip 192.168.1.101/24 --gateway 192.168.1.1 \
  --plan small --ssh-key mykey --password 's3cret'
```

LXC plans are `micro` / `small` / `medium` / `large` (memory goes down to 512 MB);
see `~/.hlab/plans.yaml`.

## Login is root; prefer a static IP

Containers **log in as root**. A **static IP is recommended**: containers have no
guest agent, so hlab can't auto-discover a DHCP address. A DHCP container is
created fine, but `provision` / `ssh` then need a known IP.

For a container's SSH access, an SSH key is strongly recommended — a container
created with no key is console-only until a key is injected. See
[SSH keys](ssh-keys.md).

## Static IP on Proxmox 9.1+

On Proxmox 9.1+, hlab configures container networking as `host_managed` static IP,
matching what the web UI does.

## Nesting is always on

**Nesting is always enabled** (matching the Proxmox web UI). Modern systemd guests
misbehave in an unprivileged container without it — getty units crash-loop and
console login becomes impossible — and it also lets Docker/Podman run inside. So it
is not a choice: there is no `--nesting` flag.

## Snapshots, resize, migration

All day-2 ops work:

- **Snapshots** have no RAM state (containers have none).
- **Resize** grows the rootfs / changes cores/RAM in place (disk grows only).
- **Migration** goes through the Proxmox API — a running container is restarted, as
  containers can't live-migrate.

Caveat: a container with snapshots on **local (non-shared) storage** can't be
migrated across nodes — Proxmox refuses because LVM-thin snapshots aren't
migratable. hlab pre-flights this and gives a clear error; delete the snapshots
first, or use shared storage.
