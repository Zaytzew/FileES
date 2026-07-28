package realmalias

import "testing"

func TestNormalizeAcceptsOnlySafeCanonicalAliases(t *testing.T) {
	got, err := Normalize("  Acme-K_02  ")
	if err != nil || got != "acme-k_02" {
		t.Fatalf("Normalize() = %q, %v", got, err)
	}
	for _, unsafe := range []string{"ab", "a@b", "a b", "a/b", "a$(id)", "a`id`", "żaba", "-root", "root-", "a\x00b"} {
		if _, err := Normalize(unsafe); err == nil {
			t.Errorf("unsafe alias %q accepted", unsafe)
		}
	}
}
