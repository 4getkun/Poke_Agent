package server

import "testing"

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "''"},
		{name: "plain", input: "argument", want: "'argument'"},
		{name: "spaces", input: "path with spaces", want: "'path with spaces'"},
		{name: "single quote", input: "it's", want: "'it'\\''s'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellQuote(tt.input); got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
