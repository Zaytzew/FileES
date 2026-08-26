package main

import "testing"

func TestLinuxColorSchemeIsDark(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"'prefer-dark'\n", true},
		{"\"PREFER-DARK\"", true},
		{"'default'\n", false},
		{"prefer-light", false},
		{"", false},
	}
	for _, test := range tests {
		if got := linuxColorSchemeIsDark(test.value); got != test.want {
			t.Errorf("linuxColorSchemeIsDark(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestSystemThemeScript(t *testing.T) {
	if got := systemThemeScript(true); got != `document.documentElement.dataset.systemTheme="dark";` {
		t.Fatalf("dark script = %q", got)
	}
	if got := systemThemeScript(false); got != `document.documentElement.dataset.systemTheme="light";` {
		t.Fatalf("light script = %q", got)
	}
}
