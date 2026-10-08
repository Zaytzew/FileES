//go:build windows && !nocfapi

package main

import (
	"bytes"
	"testing"

	"filees/internal/anchorrepair"
)

func TestAnchorRepairExitCodeSaysWhetherTheFilterIsBack(t *testing.T) {
	for _, test := range []struct {
		name   string
		result anchorrepair.Result
		want   int
	}{
		{"done", anchorrepair.Result{Detached: true, Reattached: true}, anchorRepairDone},
		{"nothing to do", anchorrepair.Result{}, anchorRepairDone},
		{"no rights", anchorrepair.Result{Error: "elevation_required"}, anchorRepairNoRights},
		{"not ours", anchorrepair.Result{Error: "not_anchor"}, anchorRepairRefused},
		{"refused path", anchorrepair.Result{Error: "refused_path"}, anchorRepairRefused},
		{"detach failed", anchorrepair.Result{Error: "detach"}, anchorRepairFailed},
		{"some stayed", anchorrepair.Result{Error: "remove", Detached: true, Reattached: true}, anchorRepairIncomplete},
		{"filter not back", anchorrepair.Result{Error: "attach", Detached: true}, anchorRepairNotAttached},
		{"filter not back wins", anchorrepair.Result{Error: "remove", Detached: true}, anchorRepairNotAttached},
		{"reattach alone failed", anchorrepair.Result{Error: "attach"}, anchorRepairNotAttached},
	} {
		if got := anchorRepairExit(test.result); got != test.want {
			t.Errorf("%s: exit %d, want %d", test.name, got, test.want)
		}
	}
}

func TestAnchorRepairCommandTakesOnlyAFolder(t *testing.T) {
	for _, args := range [][]string{nil, {"--root"}, {"--root", `F:\a`, "extra"}, {"--entries", "x"}} {
		var out bytes.Buffer
		if code := runAnchorRepair(args, anchorrepair.Windows{}, &out); code != anchorRepairUsage || out.Len() != 0 {
			t.Errorf("%q: exit %d, output %q", args, code, out.String())
		}
	}
	var out bytes.Buffer
	if code := runAnchorRepair([]string{"--root", t.TempDir()}, anchorrepair.Windows{}, &out); code != anchorRepairRefused {
		t.Errorf("ordinary folder: exit %d, output %q", code, out.String())
	}
}
