# Proxmox API token (least privilege)

hlab authenticates with a scoped Proxmox API token — it never needs your root
password. Create a dedicated role + user + token once (on Proxmox 9):

```bash
pveum role add HLab -privs "Datastore.AllocateSpace Datastore.Audit Pool.Allocate \
  Sys.Audit Sys.Console Sys.Modify VM.Allocate VM.Audit VM.Clone VM.Config.CDROM \
  VM.Config.Cloudinit VM.Config.CPU VM.Config.Disk VM.Config.HWType VM.Config.Memory \
  VM.Config.Network VM.Config.Options VM.Console VM.Migrate VM.PowerMgmt VM.Snapshot \
  VM.Snapshot.Rollback VM.GuestAgent.Audit VM.GuestAgent.Unrestricted SDN.Use"
pveum user add hlab@pve
pveum aclmod / -user hlab@pve -role HLab
pveum user token add hlab@pve hlab --privsep 0   # prints the secret once
```

Give the token id (`hlab@pve!hlab`) and secret to `hlab setup`.

## Why each notable privilege

- **`VM.GuestAgent.Audit`** — lets hlab read the VM's IP address from the guest
  agent. Note `VM.Monitor` was removed in Proxmox 9 — don't include it.
- **`VM.Snapshot` / `VM.Snapshot.Rollback`** — back `hlab vm snapshot | rollback |
  snapshot-delete`; without them those calls fail with
  `403 Permission check failed (…, VM.Snapshot)`.
- **`VM.Console`** — lets hlab inject the first SSH key into a **keyless LXC
  container** over the Proxmox console (termproxy). Without it, `ct add-ssh-key`
  on a keyless container fails with `403 … VM.Console`. See [SSH keys](ssh-keys.md).
- **`VM.GuestAgent.Unrestricted`** — lets hlab inject the first SSH key into a
  **keyless VM** through the QEMU guest agent (the VM analogue of the container
  console path). Without it, `vm add-ssh-key` on a keyless VM fails with
  `403 … VM.GuestAgent.Unrestricted`. It also needs `qemu-guest-agent` running in
  the VM (hlab's golden images set `agent=1`). See [SSH keys](ssh-keys.md).

## Adding privileges to an existing role

If you created the role for an older hlab, append the newer privileges instead of
recreating it:

```bash
pveum role modify HLab --privs "VM.Snapshot,VM.Snapshot.Rollback" --append 1
pveum role modify HLab --privs "VM.Console" --append 1
pveum role modify HLab --privs "VM.GuestAgent.Unrestricted" --append 1
```

## LXC containers

LXC containers use the same `VM.*` / `Datastore.*` privileges as VMs (Proxmox
scopes container permissions under the same tree), so the role above also covers
`hlab ct`. If your token can create VMs, it can create containers.
