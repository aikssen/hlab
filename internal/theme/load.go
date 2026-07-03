package theme

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/aikssen/hlab/assets"
	"github.com/aikssen/hlab/internal/config"
)

// This file adds user-editable, file-defined themes on top of the compiled-in
// palettes. Themes live in a YAML at Home()/themes.yaml (honoring $HLAB_HOME),
// seeded from the embedded assets/themes.yaml on first use — the same seed-from-
// embedded-asset pattern as internal/plans. File themes override or extend the
// built-ins by name; the built-ins always remain as a fallback, so a deleted or
// broken themes.yaml never breaks the binary.
//
// Import direction: theme imports config (for Home()) and assets, mirroring the
// plans package. There is no cycle — config imports only internal/state, and
// neither config, state nor assets import theme.

// fileTheme is the on-disk representation of one theme: a name plus the ten
// semantic color roles as strings (an ANSI-256 number like "12" or a hex like
// "#bd93f9"). Any empty field falls back to the default palette's value.
type fileTheme struct {
	Name    string `yaml:"name"`
	Accent  string `yaml:"accent"`
	Text    string `yaml:"text"`
	Dim     string `yaml:"dim"`
	Good    string `yaml:"good"`
	Warn    string `yaml:"warn"`
	Bad     string `yaml:"bad"`
	Track   string `yaml:"track"`
	ModalBG string `yaml:"modal_bg"`
	OutBG   string `yaml:"out_bg"`
	OutFG   string `yaml:"out_fg"`
}

// themesDoc is the on-disk themes file: a list of themes under `themes:`.
type themesDoc struct {
	Themes []fileTheme `yaml:"themes"`
}

// palette resolves a fileTheme into a full Palette, filling any empty color field
// from the built-in default palette so a custom theme can set only the roles it
// cares about (e.g. just accent).
func (ft fileTheme) palette() Palette {
	d := palettes["default"]
	color := func(v string, fallback lipgloss.Color) lipgloss.Color {
		if v = strings.TrimSpace(v); v != "" {
			return lipgloss.Color(v)
		}
		return fallback
	}
	return Palette{
		Accent:  color(ft.Accent, d.Accent),
		Text:    color(ft.Text, d.Text),
		Dim:     color(ft.Dim, d.Dim),
		Good:    color(ft.Good, d.Good),
		Warn:    color(ft.Warn, d.Warn),
		Bad:     color(ft.Bad, d.Bad),
		Track:   color(ft.Track, d.Track),
		ModalBG: color(ft.ModalBG, d.ModalBG),
		OutBG:   color(ft.OutBG, d.OutBG),
		OutFG:   color(ft.OutFG, d.OutFG),
	}
}

// Set is a merged, name-keyed collection of palettes: the compiled-in built-ins
// plus whatever the on-disk themes.yaml adds or overrides. It is the source of
// truth for the TUI selector and the `hlab theme` command.
type Set struct {
	byName map[string]Palette
}

// builtinSet returns a Set seeded with copies of the compiled-in palettes. Used
// as the base for a Load (file themes merge on top) and as the standalone
// fallback whenever the file can't be read or parsed.
func builtinSet() *Set {
	m := make(map[string]Palette, len(palettes))
	maps.Copy(m, palettes)
	return &Set{byName: m}
}

// set adds or overrides a named palette (name normalized like Get).
func (s *Set) set(name string, p Palette) {
	s.byName[strings.ToLower(strings.TrimSpace(name))] = p
}

// Get returns the palette for name (case-insensitive, space-tolerant). An unknown
// or empty name falls back to the default palette — theme selection never errors.
func (s *Set) Get(name string) Palette {
	if p, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]; ok {
		return p
	}
	return palettes["default"]
}

// Has reports whether name is a known theme in the set (case-insensitive).
func (s *Set) Has(name string) bool {
	_, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// Names returns the sorted list of theme names in the set (built-ins + file
// themes), so the TUI selector and `hlab theme` list them in a stable order.
func (s *Set) Names() []string {
	names := make([]string, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Path returns the location of the themes file (Home()/themes.yaml, mirroring
// config.Path / plans.Path — same directory, honoring $HLAB_HOME).
func Path() (string, error) {
	h, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "themes.yaml"), nil
}

// Load reads the merged theme set: the compiled-in built-ins with the on-disk
// themes.yaml merged on top (overriding/extending by name), seeding the file from
// the embedded default the first time. It ALWAYS returns a usable Set (never nil):
// on any path/read/parse error it returns the built-in-only set alongside the
// error, so a broken or deleted themes.yaml can never break the TUI or the CLI.
// Callers that only need the palette can ignore the error.
func Load() (*Set, error) {
	s := builtinSet()
	p, err := Path()
	if err != nil {
		return s, err
	}
	data, err := readOrSeed(p)
	if err != nil {
		return s, err
	}
	var doc themesDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return s, fmt.Errorf("parsing %s: %w", p, err)
	}
	for _, ft := range doc.Themes {
		if strings.TrimSpace(ft.Name) == "" {
			continue
		}
		s.set(ft.Name, ft.palette())
	}
	return s, nil
}

// readOrSeed returns the themes file's bytes, writing the embedded default first
// when the file is absent (so an editable copy always exists on disk after first
// use — the plans.loadDoc seeding pattern).
func readOrSeed(p string) ([]byte, error) {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, assets.ThemesDefault, 0o644); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(p)
}
