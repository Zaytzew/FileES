package updater

import "testing"

func TestResidentRestartHintNamesRcctlOnlyOnOpenBSD(t *testing.T) {
	services := []string{"filees_public_authority", "filees_links"}
	if got := residentRestartHint("openbsd", services); got != "rcctl restart filees_public_authority filees_links" {
		t.Fatal(got)
	}
	if got := residentRestartHint("linux", services); got != "if you run them, restart with your service manager: filees_public_authority filees_links" {
		t.Fatal(got)
	}
}
