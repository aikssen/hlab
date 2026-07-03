// Package assets embeds the Terraform workspace, the software catalog and (later)
// Ansible content into the hlab binary, so the tool is self-contained and
// distributable. hlab materializes these into the homelab-state workspace at run
// time.
package assets

import (
	"embed"
	"io/fs"
)

//go:embed all:terraform
var terraformFS embed.FS

//go:embed all:ansible
var ansibleFS embed.FS

//go:embed additional-software.yaml
var SoftwareCatalog []byte

//go:embed plans.yaml
var PlansDefault []byte

//go:embed themes.yaml
var ThemesDefault []byte

// Terraform returns the embedded Terraform workspace as a filesystem rooted at
// the "terraform" directory.
func Terraform() (fs.FS, error) {
	return fs.Sub(terraformFS, "terraform")
}

// Ansible returns the embedded Ansible content rooted at the "ansible" directory.
func Ansible() (fs.FS, error) {
	return fs.Sub(ansibleFS, "ansible")
}
