package main

import (
	"strings"
	"testing"
)

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

func TestEveryHorizontalWordmarkUsesThemeSurface(t *testing.T) {
	for _, name := range []string{"frontend/index.html", "frontend/prompt.html"} {
		data, err := frontend.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "filees-space-horizontal.svg") || !strings.Contains(text, "filees-wordmark-surface") {
			t.Fatalf("%s does not apply the shared wordmark surface", name)
		}
	}
	css, err := frontend.ReadFile("frontend/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `:root[data-system-theme="dark"] .filees-wordmark-surface { background:transparent; }`) {
		t.Fatal("dark-mode wordmark surface rule is missing")
	}
}

func TestRadarRepositoryLabelUsesPolishPlural(t *testing.T) {
	data, err := frontend.ReadFile("frontend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `plural(repos.length, "repozytorium", "repozytoria", "repozytoriów")`) {
		t.Fatal("radar repository label is not pluralised")
	}
}
