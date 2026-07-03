// hlab is a CLI/TUI that creates and provisions Proxmox VMs. It discovers
// infrastructure via the Proxmox API, asks only what cannot be inferred, and
// orchestrates Terraform (and, later, Ansible) under the hood.
package main

import "github.com/aikssen/hlab/cmd"

func main() {
	cmd.Execute()
}
