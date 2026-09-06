package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLockObservationRequiresTargetAndServerAnswer(t *testing.T) {
	wc := t.TempDir()
	path := filepath.Join(wc, "doc")
	good := `<status><target path="doc"><entry path="doc"><wc-status item="normal"><lock><token>old</token><owner>client</owner><comment>old</comment></lock></wc-status></entry><against revision="3"/></target></status>`
	obs, err := parseLockObservation(good, wc, path)
	if err != nil || obs.Local == nil || obs.Remote != nil {
		t.Fatalf("stale WC fallback: %+v %v", obs, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `<against revision="3"/>`, "", 1),
		strings.Replace(good, `revision="3"`, `revision="bad"`, 1),
		strings.Replace(good, `entry path="doc"`, `entry path="other"`, 1),
		strings.Replace(good, `item="normal"`, `item="unversioned"`, 1),
		strings.Replace(good, `</target>`, `<entry path="other"/></target>`, 1),
		strings.Replace(good, `<token>old</token>`, `<token/>`, 1),
		`<status/>`, "malformed",
	} {
		if _, err := parseLockObservation(bad, wc, path); err == nil {
			t.Fatalf("unsafe absence accepted: %s", bad)
		}
	}
}
