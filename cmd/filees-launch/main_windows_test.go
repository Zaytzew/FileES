//go:build windows

package main

import "testing"

func TestLauncherArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--show"}, {"--supervise"}, {"--supervise", "--interactive"}} {
		if _, ok := launchMode(args); !ok {
			t.Fatalf("rejected %q", args)
		}
	}
	for _, args := range [][]string{{"--hide"}, {"--show", "--show"}, {"-ShowGUI"}, {"--supervise", "arbitrary"}} {
		if code := run(args); code != 2 {
			t.Fatalf("accepted %q: %d", args, code)
		}
	}
}
