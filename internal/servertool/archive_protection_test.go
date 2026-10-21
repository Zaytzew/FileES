package servertool

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/passport"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestArchiveHookModesAndEarlyLockRefusal(t *testing.T) {
	old := sandboxNarrow
	t.Cleanup(func() { sandboxNarrow = old })
	var promises string
	sandboxNarrow = func(value string) error { promises = value; return nil }
	var out, stderr bytes.Buffer
	handled, code := RunLockGuardMode("filees-worker", []string{"--archive-guard-version"}, &out, &stderr)
	if !handled || code != ExitOK || out.String() != repoworker.ArchiveGuardVersion+"\n" || promises != "stdio" {
		t.Fatal("guard negotiation", handled, code, out.String(), promises)
	}
	repo := filepath.Join(t.TempDir(), uuid.NewString())
	// A required but incomplete policy must stop pre-lock before the passport
	// execution journal is consulted or mutated. No metadata repair is implied.
	if err := os.MkdirAll(filepath.Join(repo, "hooks", ".filees-archive-protection"), 0755); err != nil {
		t.Fatal(err)
	}
	meta := passport.Metadata{AcquisitionID: uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute), HardExpiresAt: time.Now().Add(time.Hour)}
	out.Reset()
	stderr.Reset()
	handled, code = RunLockGuardMode("pre-lock", []string{repo, "/doc", uuid.NewString(), passport.FormatComment(meta), "0"}, &out, &stderr)
	if !handled || code != ExitUnavailable || out.Len() != 0 || !strings.Contains(stderr.String(), "archive lock admission") || strings.Contains(stderr.String(), "lock acquisition admission") {
		t.Fatal("passport entered before archive guard", handled, code, stderr.String())
	}
	stderr.Reset()
	handled, code = RunRealmQuotaMode("pre-revprop-change", []string{repo, "1", "user", "svn:log", "M"}, &stderr)
	if !handled || code == 0 || promises != "stdio rpath" || !strings.Contains(stderr.String(), "administrative") {
		t.Fatal("revprop dispatch", handled, code, stderr.String(), promises)
	}
}
