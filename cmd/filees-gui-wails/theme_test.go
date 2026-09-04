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

func TestCleanupLayoutKeepsServerStateAndActionsInMainPanel(t *testing.T) {
	index, err := frontend.ReadFile("frontend/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := frontend.ReadFile("frontend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := frontend.ReadFile("frontend/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`id="pair-mobile"`, `id="pulse-core"`, `id="pulse-inner-ring"`} {
		if !strings.Contains(string(index), wanted) {
			t.Fatalf("main panel is missing %q", wanted)
		}
	}
	if !strings.Contains(string(index), `class="icon-button hint-button pair-button"`) || !strings.Contains(string(index), `<span>Paruj</span>`) {
		t.Fatal("mobile pairing is missing its wide labelled topbar control")
	}
	if !strings.Contains(string(index), `<h2>Serwery</h2>`) {
		t.Fatal("repository projection is not labelled as a server list")
	}
	if strings.Contains(string(index), `id="connection"`) {
		t.Fatal("separate connection pill survived radar consolidation")
	}
	for _, wanted := range []string{"expandedServers", "data-toggle-server", "working_copy_size_known", "repo-icon-action", "Rozmiar", "repo-state-overlay", "Połącz z lokalnym folderem", "<span>Akcje</span>", `renderRepoGroup("Zdalne", remote, "remote")`, `M3 11v2a2 2 0 0 0 2 2h2l4 4V5`} {
		if !strings.Contains(string(script), wanted) {
			t.Fatalf("cleanup renderer is missing %q", wanted)
		}
	}
	if strings.Contains(string(script), "state-pill") || strings.Contains(string(styles), ".repo-actions") {
		t.Fatal("obsolete state pill or leading action column survived folder-row cleanup")
	}
	if !strings.Contains(string(script), `repo.can_attach`) || !strings.Contains(string(styles), "padding-right:39px") {
		t.Fatal("remote pin or one-slot size inset is missing")
	}
	if !strings.Contains(string(script), `.repo-name strong`) || !strings.Contains(string(script), "tools.children") || !strings.Contains(string(script), "document.createRange()") {
		t.Fatal("column widths are not based on the folder name and actual action buttons")
	}
	if strings.Contains(string(script), "name?.scrollWidth") {
		t.Fatal("folder width measurement still feeds the assigned grid width back into the next render")
	}
	for _, wanted := range []string{".hint-button::after", ".server-panel.has-attention", ".server-folders[hidden]", ".repo-state-overlay", ".orbit-c"} {
		if !strings.Contains(string(styles), wanted) {
			t.Fatalf("cleanup styles are missing %q", wanted)
		}
	}
	for _, wanted := range []string{"serverHealthPresentation(server.health)", "health-current", "health-unavailable"} {
		if !strings.Contains(string(script), wanted) {
			t.Fatalf("per-server health rendering is missing %q", wanted)
		}
	}
	for _, wanted := range []string{".server-mark.health-current", ".server-mark.health-unavailable"} {
		if !strings.Contains(string(styles), wanted) {
			t.Fatalf("per-server health styling is missing %q", wanted)
		}
	}
	if strings.Contains(string(styles), ".server-panel.has-attention .server-mark") {
		t.Fatal("repository attention still overrides the server health indicator")
	}
	if !strings.Contains(string(styles), ".orbit-c { inset:58px; border-width:8px;") || !strings.Contains(string(styles), "animation:spin 7.5s linear infinite") {
		t.Fatal("radar is missing the broad rotating inner ring")
	}
}

func TestDashboardStartsServersExpandedAndProjectsLocksAndShares(t *testing.T) {
	index, err := frontend.ReadFile("frontend/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := frontend.ReadFile("frontend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := frontend.ReadFile("frontend/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`id="metric-reservations"`, `id="metric-public-shares"`, "Wypożyczenia", "Udostępnienia"} {
		if !strings.Contains(string(index), wanted) {
			t.Fatalf("dashboard metrics are missing %q", wanted)
		}
	}
	for _, wanted := range []string{`data-toggle-card="public-shares-body"`, `data-toggle-card="update-body"`, `data-toggle-card="reservations-body"`, `data-toggle-card="shouts-body"`, `data-toggle-card="activity-body"`, `aria-expanded="true"`} {
		if !strings.Contains(string(index), wanted) {
			t.Fatalf("expandable side cards are missing %q", wanted)
		}
	}
	for _, wanted := range []string{"seenServers", "expandedServers.add(server.id)", `share.state === "active"`, "snapshot.reservation_status", "snapshot.public_shares_known"} {
		if !strings.Contains(string(script), wanted) {
			t.Fatalf("dashboard projection is missing %q", wanted)
		}
	}
	for _, wanted := range []string{`closest("[data-toggle-card]")`, `body.hidden = expanded`, `toggle.setAttribute("aria-expanded", String(!expanded))`} {
		if !strings.Contains(string(script), wanted) {
			t.Fatalf("side-card toggle behaviour is missing %q", wanted)
		}
	}
	for _, wanted := range []string{"repeat(6,minmax(0,1fr))", "repeat(6,minmax(180px,1fr))", "scroll-snap-type:x proximity"} {
		if !strings.Contains(string(styles), wanted) {
			t.Fatalf("six-field metric carousel is missing %q", wanted)
		}
	}
	if !strings.Contains(string(styles), `.side-card-body[hidden]`) || !strings.Contains(string(styles), `.side-card-toggle[aria-expanded="false"]`) {
		t.Fatal("side-card collapsed styles are missing")
	}
}
