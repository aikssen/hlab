# One LXC container per entry in var.cts. Uses the bpg
# proxmox_virtual_environment_container resource (a separate schema from the VM
# resource): created from a container template (vztmpl volid), no BIOS, no agent,
# no emulated hardware. Static IP is recommended — containers have no QEMU agent,
# so a DHCP-assigned address cannot be auto-discovered by hlab.

resource "proxmox_virtual_environment_container" "ct" {
  for_each = var.cts

  node_name     = each.value.node
  vm_id         = each.value.vmid
  unprivileged  = each.value.unprivileged
  start_on_boot = true
  started       = true

  cpu {
    cores = each.value.cores
  }

  memory {
    dedicated = each.value.memory_mb
    swap      = each.value.swap_mb
  }

  disk {
    datastore_id = each.value.storage
    size         = each.value.disk_gb
  }

  network_interface {
    name   = "eth0"
    bridge = each.value.bridge
    # PVE 9.1+: when set, Proxmox writes the container's network config itself.
    # hlab sets this for static-IP containers on 9.1+ (see engine.Create); it is
    # null (omitted) otherwise so the parameter is never sent to older Proxmox.
    host_managed = each.value.host_managed
  }

  # operating_system is only rendered when template_file is set: hlab always
  # fills it in for containers it creates, and leaves it "" for an adopted
  # container (it already exists — there is no template to create it from).
  dynamic "operating_system" {
    for_each = each.value.template_file != "" ? [1] : []
    content {
      template_file_id = each.value.template_file
      type             = each.value.os_type
    }
  }

  # operating_system is entirely ForceNew and irrelevant once the container
  # exists. initialization[0].user_account is also ForceNew and unreadable via
  # the API — an import always comes back with it null, so without this ignore
  # every adopted (or already-created) container would plan a replace just
  # because hlab renders a user_account block. Note: started = true /
  # start_on_boot = true above mean an adopted container that was stopped when
  # found will be started by the next apply (expected — surfaced as a warning
  # during `hlab ct adopt`).
  lifecycle {
    ignore_changes = [operating_system, initialization[0].user_account]
  }

  features {
    nesting = each.value.nesting
  }

  initialization {
    hostname = each.key

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
      password = lookup(var.ct_passwords, each.key, "") != "" ? var.ct_passwords[each.key] : null
      keys     = each.value.ssh_keys
    }
  }
}
