# Discovered IPv4 addresses (reported by the QEMU guest agent), keyed by VM name.
# LXC containers have no guest agent and never appear here; hlab uses their
# declared static IP instead.
output "ip_addresses" {
  description = "First non-loopback IPv4 address per VM, when the guest agent reports it."
  value = {
    for name, vm in proxmox_virtual_environment_vm.vm :
    name => try(vm.ipv4_addresses, [])
  }
}
