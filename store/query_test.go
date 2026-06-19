package store

import "testing"

func TestEscapeLike(t *testing.T) {
	tests := map[string]string{
		"plain":       "plain",
		`100%_ready`:  `100\%\_ready`,
		`path\value%`: `path\\value\%`,
	}
	for input, want := range tests {
		if got := EscapeLike(input); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", input, got, want)
		}
	}
}
