package serverconfig

import "testing"

func TestRepositoryPolicyDefaultsAndExplicitDataErasureWindow(t *testing.T) {
	var repository RepositoryFile
	if got := repository.EffectiveDataErasureMaxDays(); got != 90 {
		t.Fatalf("default data-erasure window = %d, want 90", got)
	}
	days := 180
	repository.DataErasureMaxDays = &days
	if got := repository.EffectiveDataErasureMaxDays(); got != days {
		t.Fatalf("explicit data-erasure window = %d, want %d", got, days)
	}
}
