package software

import "testing"

func TestCatalogParsesEmbeddedYAML(t *testing.T) {
	items, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog() error: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("Catalog() returned no items")
	}

	seen := map[string]bool{}
	for _, it := range items {
		if it.Key == "" {
			t.Errorf("catalog item has an empty key: %+v", it)
		}
		if it.Label == "" {
			t.Errorf("catalog item %q has an empty label", it.Key)
		}
		if seen[it.Key] {
			t.Errorf("duplicate catalog key %q", it.Key)
		}
		seen[it.Key] = true
	}
}

func TestSelectable(t *testing.T) {
	items := []Item{
		{Key: "docker", Label: "Docker"},
		{Key: DotfilesKey, Label: "Dotfiles"},
		{Key: "node", Label: "Node", Mise: true},
	}

	t.Run("keeps dotfiles first when configured", func(t *testing.T) {
		got := Selectable(items, true)
		if len(got) != len(items) {
			t.Fatalf("Selectable(configured) len = %d, want %d", len(got), len(items))
		}
		if got[0].Key != DotfilesKey {
			t.Errorf("Selectable(configured)[0] = %q, want dotfiles first", got[0].Key)
		}
		// The other entries keep their relative order after dotfiles.
		if got[1].Key != "docker" || got[2].Key != "node" {
			t.Errorf("Selectable(configured) order = %q, %q; want docker, node after dotfiles", got[1].Key, got[2].Key)
		}
	})

	t.Run("dotfiles-first even when it is last in the catalog", func(t *testing.T) {
		in := []Item{{Key: "docker"}, {Key: "node", Mise: true}, {Key: DotfilesKey}}
		got := Selectable(in, true)
		if got[0].Key != DotfilesKey {
			t.Errorf("Selectable dotfiles-first: got[0] = %q, want %q", got[0].Key, DotfilesKey)
		}
	})

	t.Run("drops dotfiles when not configured", func(t *testing.T) {
		got := Selectable(items, false)
		if len(got) != len(items)-1 {
			t.Fatalf("Selectable(unconfigured) len = %d, want %d", len(got), len(items)-1)
		}
		for _, it := range got {
			if it.Key == DotfilesKey {
				t.Error("Selectable(unconfigured) kept the dotfiles entry")
			}
		}
	})

	t.Run("no dotfiles entry present is a no-op", func(t *testing.T) {
		in := []Item{{Key: "docker"}, {Key: "node"}}
		if got := Selectable(in, false); len(got) != len(in) {
			t.Errorf("Selectable(no dotfiles) len = %d, want %d", len(got), len(in))
		}
	})
}

func TestRequiresMise(t *testing.T) {
	cat, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog() error: %v", err)
	}
	var miseKey, nonMiseKey string
	for _, it := range cat {
		if it.Mise && miseKey == "" {
			miseKey = it.Key
		}
		if !it.Mise && nonMiseKey == "" {
			nonMiseKey = it.Key
		}
	}
	if miseKey == "" {
		t.Skip("no mise-managed catalog entry to test against")
	}

	t.Run("true when a mise-managed key is selected", func(t *testing.T) {
		got, err := RequiresMise([]string{miseKey})
		if err != nil {
			t.Fatalf("RequiresMise() error: %v", err)
		}
		if !got {
			t.Errorf("RequiresMise([%q]) = false, want true", miseKey)
		}
	})

	t.Run("false when nothing selected", func(t *testing.T) {
		got, err := RequiresMise(nil)
		if err != nil {
			t.Fatalf("RequiresMise() error: %v", err)
		}
		if got {
			t.Error("RequiresMise(nil) = true, want false")
		}
	})

	t.Run("false for an unknown key", func(t *testing.T) {
		got, err := RequiresMise([]string{"totally-unknown-key"})
		if err != nil {
			t.Fatalf("RequiresMise() error: %v", err)
		}
		if got {
			t.Error("RequiresMise(unknown key) = true, want false")
		}
	})

	if nonMiseKey != "" {
		t.Run("false when only a non-mise key is selected", func(t *testing.T) {
			got, err := RequiresMise([]string{nonMiseKey})
			if err != nil {
				t.Fatalf("RequiresMise() error: %v", err)
			}
			if got {
				t.Errorf("RequiresMise([%q]) = true, want false", nonMiseKey)
			}
		})
	}
}
