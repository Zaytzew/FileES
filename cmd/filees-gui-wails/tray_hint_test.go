package main

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
)

func TestTrayHintExplainsPreservedCopiesInsteadOfHistoricalLogs(t *testing.T) {
	vm := guiapp.ViewModel{Connected: true, Icon: guiapp.IconError, Repos: []guiapp.RepoViewModel{
		{ID: "a", DisplayName: "AKTUALNE", ServerID: "cloud", LocalCopyPreserved: true, LocalCopyStatus: "clean", LocalCleanupPending: true},
		{ID: "b", DisplayName: "EKOPROJEKT", ServerID: "spot", LocalCopyPreserved: true, LocalCopyStatus: "changed"},
	}}
	snapshot := projectViewModel(vm, journal.Texts{})
	snapshot.Errors = []ErrorProjection{{Message: "RAW OLD LOG", Code: "NET-4007"}}
	got := projectWailsTray(snapshot)
	for _, want := range []string{"AKTUALNE: sprzątanie metadanych czeka", "EKOPROJEKT: repo usunięte"} {
		if !strings.Contains(got.Tooltip, want) {
			t.Fatalf("missing %q in %q", want, got.Tooltip)
		}
	}
	if strings.Contains(got.Tooltip, "RAW OLD LOG") || strings.Contains(got.Tooltip, "NET-4007") {
		t.Fatal("historical log presented as colour cause")
	}
	if got.Icon != guiapp.IconError {
		t.Fatal("tooltip changed icon")
	}
}

func TestTrayHintLimitsAndSanitizesNames(t *testing.T) {
	vm := guiapp.ViewModel{Connected: true, Icon: guiapp.IconError}
	for i := 0; i < 5; i++ {
		vm.Repos = append(vm.Repos, guiapp.RepoViewModel{ID: string(rune('a' + i)), DisplayName: strings.Repeat("ż", 80) + "\nFAKE", LocalCopyPreserved: true, LocalCopyStatus: "changed"})
	}
	got := projectWailsTray(projectViewModel(vm, journal.Texts{})).Tooltip
	if strings.Contains(got, "\nFAKE") || (runtime.GOOS != "windows" && !strings.Contains(got, "2 kolejnych")) {
		t.Fatalf("bad bounded hint: %q", got)
	}
	limited := limitTrayHint(strings.Repeat("🖊", 150), 127)
	if len(utf16.Encode([]rune(limited))) > 127 || !strings.HasSuffix(limited, "(więcej w panelu)") {
		t.Fatal("UTF16 tooltip overflow")
	}
}
