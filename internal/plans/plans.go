// Package plans manages hlab's preconfigured VM "plans" (t-shirt sizes such as
// KVM1/KVM2/KVM4/KVM8). The plans live in a user-editable YAML at
// ~/.hlab/plans.yaml (override the home dir with $HLAB_HOME), seeded from an
// embedded default on first use, so the operator can change the offered sizes
// without rebuilding hlab.
package plans

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aikssen/hlab/assets"
	"github.com/aikssen/hlab/internal/config"
)

// ParseMem parses a memory size into megabytes. The default unit is GB (matching
// VMs): a bare number is gigabytes, fractional allowed ("0.5" = 512 MB). An M/MB
// suffix is the explicit sub-GB case ("512M" = 512 MB); a G/GB suffix is also
// accepted. Used for LXC memory inputs so the unit is consistent with VMs and MB
// is the special case that carries a suffix.
func ParseMem(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("required")
	}
	mult := 1024.0 // default unit is GB
	switch {
	case strings.HasSuffix(s, "gb"):
		s = strings.TrimSuffix(s, "gb")
	case strings.HasSuffix(s, "g"):
		s = strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "mb"):
		s, mult = strings.TrimSuffix(s, "mb"), 1
	case strings.HasSuffix(s, "m"):
		s, mult = strings.TrimSuffix(s, "m"), 1
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("enter a size in GB like 1 or 2, or MB like 512M")
	}
	return int(f * mult), nil
}

// FormatMem renders a memory size (in MB) back into an input string: whole
// gigabytes as a bare number ("2048" → "2"), otherwise the explicit MB form
// ("512" → "512M"). The inverse of ParseMem for the common cases, used to
// pre-fill edit forms.
func FormatMem(mb int) string {
	if mb > 0 && mb%1024 == 0 {
		return strconv.Itoa(mb / 1024)
	}
	return strconv.Itoa(mb) + "M"
}

// Custom is the sentinel "no plan — enter specs manually" choice.
const Custom = "custom"

// Plan is a named CPU/memory/disk size.
type Plan struct {
	Name     string `yaml:"name"`
	Label    string `yaml:"label,omitempty"` // optional display label; derived when empty
	Cores    int    `yaml:"cores"`
	MemoryGB int    `yaml:"memory_gb"`           // whole-GB sizing (VM plans)
	MemoryMB int    `yaml:"memory_mb,omitempty"` // MB sizing; preferred when set (sub-GB LXC tiers)
	DiskGB   int    `yaml:"disk_gb"`
}

// MB returns the plan's memory in megabytes, preferring the explicit MemoryMB
// when set (LXC tiers below 1 GB) and otherwise deriving it from MemoryGB.
func (p Plan) MB() int {
	if p.MemoryMB > 0 {
		return p.MemoryMB
	}
	return p.MemoryGB * 1024
}

// DisplayLabel returns the configured label, or a derived one like
// "KVM2 — 2c · 4GB · 32GB" (or "micro — 1c · 512MB · 4GB" for sub-GB tiers).
func (p Plan) DisplayLabel() string {
	if p.Label != "" {
		return p.Label
	}
	mem := fmt.Sprintf("%dGB", p.MemoryGB)
	if p.MemoryMB > 0 && p.MemoryMB%1024 != 0 {
		mem = fmt.Sprintf("%dMB", p.MemoryMB)
	} else if p.MemoryMB > 0 {
		mem = fmt.Sprintf("%dGB", p.MemoryMB/1024)
	}
	return fmt.Sprintf("%s — %dc · %s · %dGB", p.Name, p.Cores, mem, p.DiskGB)
}

// Path returns the location of the plans file (Home()/plans.yaml, mirroring
// config.Path — same directory, honoring $HLAB_HOME).
func Path() (string, error) {
	h, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "plans.yaml"), nil
}

// Load reads the VM plans (the top-level `plans:` list), seeding the file with
// the embedded default the first time (so an editable copy always exists on disk
// after first use).
func Load() ([]Plan, error) {
	doc, _, err := loadDoc()
	if err != nil {
		return nil, err
	}
	return doc.Plans, nil
}

// LoadLXC reads the LXC plans (the `lxc:` list) from the same file. When the
// on-disk file predates LXC support and has no `lxc:` section, it falls back to
// the embedded default so existing installs still get container plans without a
// manual edit.
func LoadLXC() ([]Plan, error) {
	doc, _, err := loadDoc()
	if err != nil {
		return nil, err
	}
	if len(doc.LXC) == 0 {
		var def plansDoc
		if err := yaml.Unmarshal(assets.PlansDefault, &def); err == nil {
			return def.LXC, nil
		}
	}
	return doc.LXC, nil
}

// plansDoc is the on-disk plans file: VM plans under `plans:`, LXC plans under
// `lxc:`.
type plansDoc struct {
	Plans []Plan `yaml:"plans"`
	LXC   []Plan `yaml:"lxc"`
}

// loadDoc reads and parses the plans file, seeding it from the embedded default
// on first use.
func loadDoc() (plansDoc, string, error) {
	p, err := Path()
	if err != nil {
		return plansDoc{}, "", err
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return plansDoc{}, p, err
		}
		if err := os.WriteFile(p, assets.PlansDefault, 0o644); err != nil {
			return plansDoc{}, p, err
		}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return plansDoc{}, p, err
	}
	var doc plansDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return plansDoc{}, p, fmt.Errorf("parsing %s: %w", p, err)
	}
	// Migrate a pre-M6 plans.yaml that has no `lxc:` section: append the block
	// verbatim from the embedded default so the LXC tiers become editable on
	// disk. We slice the raw bytes rather than re-marshal the whole document —
	// yaml.v3 marshalling would drop the operator's comments/edits in `plans:`.
	// Guarded by the empty-LXC check, so it runs at most once (idempotent).
	if len(doc.LXC) == 0 {
		if migrated, ok := appendLXCSection(data); ok {
			if err := os.WriteFile(p, migrated, 0o644); err != nil {
				return plansDoc{}, p, err
			}
			var m plansDoc
			if err := yaml.Unmarshal(migrated, &m); err != nil {
				return plansDoc{}, p, fmt.Errorf("parsing %s: %w", p, err)
			}
			doc = m
		}
	}
	return doc, p, nil
}

// appendLXCSection returns data with the embedded default's `lxc:` block
// appended verbatim, sliced from the "\nlxc:" marker to EOF (the leading
// newline gives a blank-line separation from the operator's content). It
// reports false when the embedded default has no such marker, so callers fall
// back to leaving the file untouched.
func appendLXCSection(data []byte) ([]byte, bool) {
	i := bytes.Index(assets.PlansDefault, []byte("\nlxc:"))
	if i < 0 {
		return nil, false
	}
	section := assets.PlansDefault[i:] // from the newline before `lxc:` to EOF
	out := data
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, section...), true
}

// ByName returns the plan with the given name (case-insensitive on the exact
// stored name is not done — names are expected as-is, e.g. "KVM2").
func ByName(ps []Plan, name string) (Plan, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return Plan{}, false
}
