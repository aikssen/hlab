# One VM resource per entry in var.vms, cloned from a golden-image template and
# configured on first boot via cloud-init. Follows the homelab standards:
# q35 / SeaBIOS / x86-64-v2-AES / VirtIO SCSI / QEMU guest agent.

resource "proxmox_virtual_environment_vm" "vm" {
  for_each = var.vms

  name      = each.key
  node_name = each.value.node
  vm_id     = each.value.vmid

  # Migrate the guest to the new node when node_name changes, instead of the
  # default destroy-and-recreate. Lets `hlab vm migrate` move a VM by updating its
  # declaration and applying, keeping the disk and the VM id.
  migrate = true

  # clone is only rendered when template_id is set: hlab always fills it in for
  # VMs it creates, and leaves it 0 for an adopted VM (which already exists, so
  # there is nothing to clone from). Rendering `vm_id = 0` would fail the
  # provider's clone-block validation.
  dynamic "clone" {
    for_each = each.value.template_id > 0 ? [1] : []
    content {
      vm_id = each.value.template_id
      full  = true
    }
  }

  # Never try to re-clone: once created (or adopted + imported), the clone
  # source is irrelevant. Without this, editing template_id in the YAML — or
  # importing a VM with template_id = 0 — would plan a destroy-and-recreate.
  lifecycle {
    ignore_changes = [clone]
  }

  machine       = "q35"
  bios          = "seabios"
  scsi_hardware = "virtio-scsi-single"

  agent {
    enabled = true
  }

  cpu {
    cores = each.value.cores
    type  = "x86-64-v2-AES"
  }

  memory {
    dedicated = each.value.memory_mb
    # Enable the virtio-balloon device (fixed: floating == dedicated, so the VM's
    # RAM is never reduced). Without it Proxmox reports the QEMU process RSS, which
    # the guest page cache inflates to ~100% after provisioning until a reboot. The
    # balloon driver reports the real (cache-excluded) usage from first boot.
    floating = each.value.memory_mb
  }

  disk {
    datastore_id = each.value.storage
    interface    = "scsi0"
    size         = each.value.disk_gb
  }

  network_device {
    bridge = each.value.bridge
    model  = "virtio"
  }

  initialization {
    datastore_id = each.value.storage

    ip_config {
      ipv4 {
        address = each.value.dhcp ? "dhcp" : each.value.ip_cidr
        gateway = each.value.dhcp ? null : each.value.gateway
      }
    }

    dynamic "dns" {
      for_each = length(each.value.dns_servers) > 0 ? [1] : []
      content {
        servers = each.value.dns_servers
      }
    }

    user_account {
      username = each.value.username
      password = lookup(var.vm_passwords, each.key, "") != "" ? var.vm_passwords[each.key] : null
      keys     = each.value.ssh_keys
    }
  }
}
