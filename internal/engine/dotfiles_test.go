package engine

import "testing"

func TestDotfilesRepoNeedsAgent(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want bool
	}{
		// SSH authenticates with a key however public the repo is, so these need
		// the forwarded agent regardless of visibility.
		{"scp-style github", "git@github.com:aikssen/dotfiles.git", true},
		{"scp-style, no .git", "git@github.com:aikssen/dotfiles", true},
		{"scp-style non-github", "git@gitlab.example.com:me/dotfiles.git", true},
		{"explicit ssh scheme", "ssh://git@github.com/aikssen/dotfiles.git", true},
		{"surrounding whitespace", "  git@github.com:aikssen/dotfiles.git  ", true},

		// A public repo over https clones anonymously — demanding an agent here
		// would block a provision that works fine.
		{"https", "https://github.com/aikssen/dotfiles.git", false},
		{"http", "http://git.example.com/dotfiles.git", false},
		{"git protocol", "git://github.com/aikssen/dotfiles.git", false},
		{"file", "file:///srv/dotfiles.git", false},

		// An https URL carrying a token has an @ in it, but its scheme still wins:
		// it is not SSH and needs no agent.
		{"https with credentials", "https://user:token@github.com/me/dotfiles.git", false},

		// Unclassifiable input errs toward not blocking.
		{"empty", "", false},
		{"bare path", "/srv/dotfiles", false},
		{"host:path with no user", "github.com:aikssen/dotfiles.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dotfilesRepoNeedsAgent(tt.repo); got != tt.want {
				t.Errorf("dotfilesRepoNeedsAgent(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}
