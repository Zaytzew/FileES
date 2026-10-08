//go:build windows

package main

import (
	"testing"

	"filees/internal/gui/actions"
)

func TestAnchorRepairArgumentsCarryOneQuotedFolder(t *testing.T) {
	for root, want := range map[string]string{
		`F:\ATMPROJEKT\02_OPRACOWANIA\ZUGA-BOISKA`: `anchor-repair --root "F:\ATMPROJEKT\02_OPRACOWANIA\ZUGA-BOISKA"`,
		`F:\Nowy folder\Faktury 2026\`:             `anchor-repair --root "F:\Nowy folder\Faktury 2026"`,
		`C:\a --reattach`:                          `anchor-repair --root "C:\a --reattach"`,
	} {
		if got, err := anchorRepairArguments(root, false); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", root, got, err, want)
		}
	}
	if got, err := anchorRepairArguments(`F:\a`, true); err != nil || got != `anchor-repair --root "F:\a" --reattach` {
		t.Errorf("reattach: %q %v", got, err)
	}
	for _, root := range []string{`relative\folder`, `F:\a" --root "C:\Windows`, "F:\a\nb", ""} {
		if got, err := anchorRepairArguments(root, false); err == nil {
			t.Errorf("%q accepted as %q", root, got)
		}
	}
}

func TestAnchorRepairExitCodesBecomeOutcomes(t *testing.T) {
	for code, want := range map[uint32]actions.AnchorRepairOutcome{
		0: actions.AnchorRepairDone, 3: actions.AnchorRepairNoRights, 6: actions.AnchorRepairIncomplete,
		7: actions.AnchorRepairFilterDetached, 2: actions.AnchorRepairFailed, 4: actions.AnchorRepairFailed,
		5: actions.AnchorRepairFailed, 1: actions.AnchorRepairFailed, 0xC0000005: actions.AnchorRepairFailed,
	} {
		if got := anchorRepairOutcome(code); got != want {
			t.Errorf("exit %d: %v, want %v", code, got, want)
		}
	}
}
