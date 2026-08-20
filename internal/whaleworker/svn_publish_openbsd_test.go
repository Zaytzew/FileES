//go:build openbsd

package whaleworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

func TestSVNPublisherRecoversAbandonedMatchingTransaction(t *testing.T) {
	svnadmin := requirePublisherTool(t, "svnadmin")
	svnlook := requirePublisherTool(t, "svnlook")
	svnmucc := requirePublisherTool(t, "svnmucc")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}
	payload := filepath.Join(root, "payload.bin")
	content := []byte("abandoned transaction recovery")
	if err := os.WriteFile(payload, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/recovered.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	j := Journal{Root: filepath.Join(root, "journal")}
	record, err := j.Create(identity, whale.DirectionPut)
	if err != nil {
		t.Fatal(err)
	}
	record.BytesHave = int64(len(content))
	record.State = whale.StateValidating
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := j.Load(identity.GenerationID)
	record = *mustRecord(t, loaded, err)
	record.State = whale.StateCommitting
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err = j.Load(identity.GenerationID)
	record = *mustRecord(t, loaded, err)

	marker := filepath.Join(root, "hook.txn")
	hook := filepath.Join(repository, "hooks", "pre-commit")
	script := "#!/bin/sh\nprintf '%s' \"$2\" > " + marker + "\nsleep 30\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--non-interactive", "--with-revprop", generationRevprop + "=" + identity.GenerationID,
		"--with-revprop", "filees:whale-sha256=" + identity.SHA256, "-m", "abandoned Whale transaction",
		"mkdir", appendURL(fileURL(repository), whale.ReservedNamespace),
		"mkdir", appendURL(fileURL(repository), whale.ReservedNamespace+"/media"),
		"put", payload, appendURL(fileURL(repository), whale.ReservedNamespace+"/media/recovered.bin"),
	}
	command := exec.Command(svnmucc, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(raw)) != "" {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Wait()
			t.Fatal("pre-commit hook did not expose the transaction")
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Wait()
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	before, err := exec.Command(svnadmin, "lstxns", repository).Output()
	if err != nil || strings.TrimSpace(string(before)) == "" {
		t.Fatalf("killed svnmucc left no inspectable transaction: %q err=%v", before, err)
	}

	publisher := SVNPublisher{SVNMucc: svnmucc, SVNLook: svnlook, SVNAdmin: svnadmin}
	revision, err := publisher.PublishWhale(context.Background(), &record, j, repository, payload)
	if err != nil || revision != 1 {
		t.Fatalf("recovered publish revision=%d err=%v", revision, err)
	}
	after, err := exec.Command(svnadmin, "lstxns", repository).Output()
	if err != nil || strings.TrimSpace(string(after)) != "" {
		t.Fatalf("abandoned transaction remains: %q err=%v", after, err)
	}
}

func requirePublisherTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s unavailable", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustRecord(t *testing.T, record *Record, err error) *Record {
	t.Helper()
	if err != nil || record == nil {
		t.Fatalf("load record=%+v err=%v", record, err)
	}
	return record
}
