//go:build windows

package main

import (
	"slices"
	"testing"
)

// The shortcuts pass nothing or --show, exactly like the wscript shim before
// it; anything else must not start a supervisor.
func TestLauncherAcceptsOnlyWhatTheShortcutsPass(t *testing.T) {
	for _, args := range [][]string{nil, {"--show"}} {
		command, ok := supervisorCommand(args)
		if !ok || !slices.Equal(command, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}) {
			t.Fatalf("supervisorCommand(%q) = %q, %v", args, command, ok)
		}
	}
	for _, args := range [][]string{{"--hide"}, {"--show", "--show"}, {"-ShowGUI"}} {
		if _, ok := supervisorCommand(args); ok {
			t.Fatalf("supervisorCommand(%q) accepted arguments no shortcut passes", args)
		}
		if code := run(args); code != 2 {
			t.Fatalf("run(%q) = %d, want 2", args, code)
		}
	}
}
