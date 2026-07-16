# VM specifications. Populated by hlab from ~/.hlab/vms/*.yaml into
# terraform.tfvars.json (non-secret). Per-VM passwords are supplied separately in
# secrets.auto.tfvars.json (gitignored) via var.vm_passwords.

variable "vms" {
  description = "Map of VMs to manage, keyed by name."
  type = map(object({
    node        = string
    vmid        = number
    template_id = number
    storage     = string
    bridge      = string
    cores       = number
    memory_mb   = number
    disk_gb     = number
    dhcp        = bool
    ip_cidr     = optional(string, "")
    gateway     = optional(string, "")
    dns_servers = optional(list(string), [])
    username    = string
    ssh_keys    = optional(list(string), [])
    # QEMU CPU model. The default is a portable baseline so a VM can live-migrate
    # between nodes with different host CPUs. Note it exposes AES but NOT
    # PCLMULQDQ, which some modern binaries require — override per cluster (see
    # cpu_type in hlab's config).
    cpu_type = optional(string, "x86-64-v2-AES")
  }))
  default = {}
}

variable "vm_passwords" {
  description = "Optional per-VM cloud-init passwords, keyed by VM name. Lives in secrets.auto.tfvars.json (gitignored)."
  type        = map(string)
  default     = {}
  sensitive   = true
}

# LXC container specifications, keyed by name. Populated by hlab alongside var.vms.
# Containers use a separate Proxmox resource with a materially different schema:
# they are created from a container template (vztmpl volume id), not cloned from a
# VM, and have no BIOS/agent/emulated hardware.
variable "cts" {
  description = "Map of LXC containers to manage, keyed by name."
  type = map(object({
    node          = string
    vmid          = number
    template_file = string
    os_type       = string
    storage       = string
    bridge        = string
    cores         = number
    memory_mb     = number
    swap_mb       = optional(number, 0)
    disk_gb       = number
    unprivileged  = optional(bool, true)
    nesting       = optional(bool, false)
    host_managed  = optional(bool)
    dhcp          = bool
    ip_cidr       = optional(string, "")
    gateway       = optional(string, "")
    dns_servers   = optional(list(string), [])
    ssh_keys      = optional(list(string), [])
  }))
  default = {}
}

variable "ct_passwords" {
  description = "Optional per-container root passwords, keyed by name. Lives in secrets.auto.tfvars.json (gitignored)."
  type        = map(string)
  default     = {}
  sensitive   = true
}
