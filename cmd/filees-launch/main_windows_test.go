//go:build windows

package main

import (
	"encoding/json"
	"filees/internal/explorermenu"
	"filees/internal/msistartup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Native CreateProcess/argv round-trip, using only a synthetic child process.
func TestMain(m *testing.M) {
	if target := os.Getenv("FILEES_TEST_EXPLORER_ARGV"); target != "" {
		request, err := explorermenu.Parse(os.Args[1:])
		if err != nil {
			os.Exit(2)
		}
		data, _ := json.Marshal(request)
		if os.WriteFile(target, data, 0600) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExplorerLaunchNativeArgumentRoundTrip(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "argv.json")
	t.Setenv("FILEES_TEST_EXPLORER_ARGV", target)
	selected := filepath.Join(root, "Żółć i zdjęcia & 100%")
	if err := launchExplorerGUI(msistartup.InstallPaths{GUI: exe, Root: root}, []string{explorermenu.Detach, selected}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(target)
		if err == nil {
			var request explorermenu.Request
			if json.Unmarshal(data, &request) != nil {
				t.Fatal("bad child output")
			}
			if request.Verb != explorermenu.Detach || request.Path != selected {
				t.Fatalf("changed argv: %+v", request)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child did not receive the gesture")
}

func TestLauncherArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--show"}, {"--supervise"}, {"--supervise", "--interactive"}, {"--prepare-upgrade", `C:\FileES`}} {
		if _, ok := launchMode(args); !ok {
			t.Fatalf("rejected %q", args)
		}
	}
	for _, args := range [][]string{{"--hide"}, {"--show", "--show"}, {"-ShowGUI"}, {"--supervise", "arbitrary"}, {"--prepare-upgrade", `C:\`}, {"--prepare-upgrade", "relative"}, {"--prepare-upgrade"}} {
		if code := run(args); code != 2 {
			t.Fatalf("accepted %q: %d", args, code)
		}
	}
}

func TestExplorerLauncherHasOnlyTwoClosedFolderGestures(t *testing.T) {
	for _, verb := range []string{explorermenu.Add, explorermenu.Detach} {
		mode, ok := launchMode([]string{verb, `D:\Żółć i zdjęcia`})
		if !ok || mode != "explorer" {
			t.Fatalf("mode=%q ok=%v", mode, ok)
		}
	}
	for _, args := range [][]string{{explorermenu.Add, ""}, {explorermenu.Detach, "relative"}, {explorermenu.Add, `D:\Folder`, "--prepare-upgrade"}, {"--explorer-delete", `D:\Folder`}} {
		if _, ok := launchMode(args); ok {
			t.Fatalf("accepted %q", args)
		}
	}
}
