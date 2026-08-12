package authority

import "testing"

func TestSVNLookEnvironmentForcesUTF8(t *testing.T) {
	got := svnLookEnvironment([]string{"PATH=/bin", "LC_ALL=C", "LANG=pl_PL.UTF-8"})
	wantLocale := 0
	for _, entry := range got {
		if entry == "LC_ALL=C.UTF-8" {
			wantLocale++
		}
		if entry == "LC_ALL=C" {
			t.Fatal("legacy LC_ALL survived in svnlook environment")
		}
	}
	if wantLocale != 1 {
		t.Fatalf("LC_ALL=C.UTF-8 count = %d, want 1", wantLocale)
	}
}
