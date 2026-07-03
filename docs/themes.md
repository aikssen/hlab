# Themes

The dashboard TUI and the CLI result boxes use a semantic color palette. Three
themes ship built-in:

- **`default`** — ANSI-256, respects your terminal's own color scheme.
- **`dracula`** — truecolor.
- **`mono`** — grayscale accent, keeping the semantic good/warn/bad colors (an
  accessibility option).

An unknown or empty name falls back to `default`.

## Switching the theme

- **Dashboard** — press `t` to open a live selector; choosing a theme applies
  immediately (re-renders) and is saved.
- **CLI** — `hlab theme` lists the themes and marks the active one; `hlab theme
  <name>` switches.
- **Config** — a `theme:` key in `~/.hlab/config.yaml` (`theme: dracula`).

## Custom themes

Themes are **data, not code**: they live in `~/.hlab/themes.yaml` (seeded from a
default on first use, honoring `$HLAB_HOME`). Edit a color or add your own theme
and it works without rebuilding hlab — the `t` selector re-reads the file each time
it opens. Each theme is a name plus ten semantic color roles; every value is a
lipgloss color (an ANSI-256 code like `"12"` or a hex like `"#bd93f9"`). Any field
you omit falls back to the `default` theme's value, so a custom theme can set just
`accent`:

```yaml
themes:
  - name: solarized
    accent: "33"
    text: "230"
```

File themes override or extend the built-ins by name; the built-ins always remain
as a fallback, so a deleted or broken `themes.yaml` never breaks hlab.
