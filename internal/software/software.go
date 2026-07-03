// Package software parses the embedded additional-software.yaml catalog used by
// the wizard's multi-select and (in M2) by the Ansible provisioner.
package software

import (
	"gopkg.in/yaml.v3"

	"github.com/aikssen/hlab/assets"
)

// DotfilesKey is the catalog key for the dotfiles entry. It is special in two
// ways other software is not: it is hidden unless a dotfiles repo is configured
// (see Selectable), and its Ansible task clones that private repo over the
// operator's forwarded SSH agent.
const DotfilesKey = "dotfiles"

// Item is one installable piece of software.
type Item struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
	Mise  bool   `yaml:"mise"` // installed via mise (a runtime)
}

// Selectable returns the catalog items offered to the user. When a dotfiles repo
// is configured the dotfiles entry is kept and hoisted to the front (it is the
// terminal environment most guests want first); otherwise it is dropped entirely
// (there is nothing to clone without a repo). Catalog() stays pure — this
// per-config filtering and ordering is a presentation concern the wizard/TUI apply.
func Selectable(items []Item, dotfilesConfigured bool) []Item {
	out := make([]Item, 0, len(items))
	var dotfiles *Item
	for i, it := range items {
		if it.Key == DotfilesKey {
			if dotfilesConfigured {
				dotfiles = &items[i]
			}
			continue
		}
		out = append(out, it)
	}
	if dotfiles != nil {
		out = append([]Item{*dotfiles}, out...)
	}
	return out
}

// Catalog returns the parsed software catalog.
func Catalog() ([]Item, error) {
	var c struct {
		Software []Item `yaml:"software"`
	}
	if err := yaml.Unmarshal(assets.SoftwareCatalog, &c); err != nil {
		return nil, err
	}
	return c.Software, nil
}

// RequiresMise reports whether any of the selected keys is a mise-managed runtime.
func RequiresMise(selected []string) (bool, error) {
	cat, err := Catalog()
	if err != nil {
		return false, err
	}
	byKey := map[string]Item{}
	for _, it := range cat {
		byKey[it.Key] = it
	}
	for _, k := range selected {
		if it, ok := byKey[k]; ok && it.Mise {
			return true, nil
		}
	}
	return false, nil
}
