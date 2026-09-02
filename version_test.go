package filees

import (
	"regexp"
	"testing"
)

func TestEmbeddedVersion(t *testing.T) {
	if got := Version(); !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(got) {
		t.Fatalf("Version() = %q, want semantic x.y.z version", got)
	}
}
