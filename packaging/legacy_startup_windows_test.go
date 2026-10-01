//go:build windows

package packaging_test

import (
	"os"
	"strings"
	"testing"
)

// Config/channel and predecessor decisions now have behavioural tests in
// internal/msistartup, including real process lifecycle tests. Pin the two
// old path adapters to the new supervisor instead of testing dead PS logic.
func TestLegacyStartupAdaptersDelegateToGo(t *testing.T) {
	for _, path := range []string{"windows/autostart-supervisor.ps1", "windows/autostart-launch.vbs"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, "filees-launch.exe") || strings.Contains(strings.ToLower(text), "powershell.exe") {
			t.Fatalf("legacy interpreter chain in %s", path)
		}
	}
	raw, err := os.ReadFile("../cmd/filees-launch/main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "powershell.exe") || strings.Contains(string(raw), "start-filees.ps1") {
		t.Fatal("launcher still delegates supervision to a script")
	}
}
