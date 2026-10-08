//go:build linux || openbsd

package servertool

import (
	"bytes"
	"encoding/json"
	"filees/internal/mobileworker"
	"filees/pkg/clientview"
	v1 "filees/pkg/mobile/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMobileAdminRecovery(t *testing.T) {
	if isolateSandboxingTest(t, "TestMobileAdminRecovery") {
		return
	}
	f := newMobileWorkerFixture(t)
	config, err := serverconfig.LoadFor(f.configPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	ledger := sandboxTempDir(t)
	if err := os.Chmod(ledger, 0700); err != nil {
		t.Fatal(err)
	}
	clientID, repoID, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
	newMobileSeededRepoAt(t, filepath.Join(f.repositoriesRoot, repoID))
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 9, []clientview.Repository{mobileGrantedRepository(repoID, "rw")})
	rec := mobileworker.Record{RequestID: id, ClientID: clientID, RepoID: repoID, Path: "mobile-uploads", State: v1.OpStateRejected}
	l := mobileworker.Ledger{Dir: ledger}
	if err := l.Put(rec); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := []string{"--request-id", id, "--ledger", ledger, "--allow-retry"}
	if code := runAdminMobileRecover(f.configPath, args, &out, &stderr); code != ExitUsage {
		t.Fatalf("unconfirmed allowed: %d %s", code, &stderr)
	}
	out.Reset()
	stderr.Reset()
	args = append(args, "--confirm-workers-stopped", "--reason", "maintenance: old workers stopped")
	command := append([]string{"-config", f.configPath, "mobile", "recover"}, args...)
	if code := RunAdmin(command, &out, &stderr); code != ExitOK {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	markers, err := os.ReadDir(filepath.Join(config.Activation.Root, "capacity-active-writers"))
	if err != nil || len(markers) != 0 {
		t.Fatalf("mobile recovery left capacity writer: %v %v", markers, err)
	}
	var got mobileworker.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.RecoveryFenced || got.State != v1.OpStateRejected {
		t.Fatalf("got %s %v", &out, err)
	}
	if runtime.GOOS == "openbsd" {
		if err := os.WriteFile(filepath.Join(f.repositoriesRoot, repoID, "recovery-must-not-write"), []byte("no"), 0600); err == nil {
			t.Fatal("recovery can write repository")
		}
	}
	info, err := os.Stat(filepath.Join(ledger, id+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
}
