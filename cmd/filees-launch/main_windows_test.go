//go:build windows

package main

import "testing"

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
