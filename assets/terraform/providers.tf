# Provider configuration for hlab-managed VMs.
#
# Credentials are NOT declared here. They are supplied at runtime via environment
# variables so secrets never touch the versioned workspace:
#   PROXMOX_VE_ENDPOINT   e.g. https://proxmox.example:8006/
#   PROXMOX_VE_API_TOKEN  e.g. root@pam!hlab=xxxxxxxx-xxxx-...
#   PROXMOX_VE_INSECURE   "true" for self-signed certificates

terraform {
  required_version = ">= 1.6"
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = "~> 0.66"
    }
  }
}

provider "proxmox" {
  # endpoint / api_token / insecure come from PROXMOX_VE_* environment variables.
}
