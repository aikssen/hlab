package engine

import "testing"

func TestKebabify(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already kebab-case", "web-01", "web-01"},
		{"uppercase is lowercased", "WEB-01", "web-01"},
		{"spaces become hyphens", "my server", "my-server"},
		{"dots become hyphens", "web.example.com", "web-example-com"},
		{"underscores become hyphens", "my_server", "my-server"},
		{"repeated separators squeeze to one hyphen", "my   server__two", "my-server-two"},
		{"leading/trailing separators trimmed", "-web-01-", "web-01"},
		{"unicode is replaced", "café-server", "caf-server"},
		{"empty string stays empty", "", ""},
		{"only separators becomes empty", "___", ""},
		{"digits preserved", "web123", "web123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kebabify(tt.in); got != tt.want {
				t.Errorf("kebabify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestKebabRegex(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid simple name", "web-01", true},
		{"valid single letter", "a", true},
		{"empty string rejected", "", false},
		{"must start with a letter, not a digit", "1web", false},
		{"must start with a letter, not a hyphen", "-web", false},
		{"uppercase rejected (kebabify should have lowercased first)", "Web", false},
		{"too long (over 63 chars) rejected", "a" + repeat("b", 63), false},
		{"exactly 63 chars accepted", "a" + repeat("b", 62), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kebabRe.MatchString(tt.in); got != tt.want {
				t.Errorf("kebabRe.MatchString(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
