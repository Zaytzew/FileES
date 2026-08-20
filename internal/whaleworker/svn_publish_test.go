package whaleworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	whale "filees/pkg/whale/v1"
	"github.com/google/uuid"
)

func TestSVNPublisherCommitsHiddenPathAndRecoversGeneration(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is unavailable")
	}
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook is unavailable")
	}
	svnmucc, err := exec.LookPath("svnmucc")
	if err != nil {
		t.Skip("svnmucc is unavailable")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}
	payload := filepath.Join(root, "payload.bin")
	content := []byte("Whale integration payload")
	if err := os.WriteFile(payload, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "03_MEDIA/Łódź/video.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	journal := Journal{Root: filepath.Join(root, "journal")}
	record, err := journal.Create(identity, whale.DirectionPut)
	if err != nil {
		t.Fatal(err)
	}
	record.BytesHave = int64(len(content))
	record.State = whale.StateValidating
	if err := journal.Save(record); err != nil {
		t.Fatal(err)
	}
	record, _ = value(journal.Load(identity.GenerationID))
	record.State = whale.StateCommitting
	if err := journal.Save(record); err != nil {
		t.Fatal(err)
	}
	record, _ = value(journal.Load(identity.GenerationID))
	publisher := SVNPublisher{SVNMucc: svnmucc, SVNLook: svnlook}
	revision, err := publisher.PublishWhale(context.Background(), &record, journal, repository, payload)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 || !record.CommitBaseKnown || record.CommitBaseRevision != 0 {
		t.Fatalf("revision=%d record=%+v", revision, record)
	}
	storagePath, _ := identity.StoragePath()
	catCommand := exec.Command(svnlook, "cat", repository, storagePath)
	catCommand.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	output, err := catCommand.CombinedOutput()
	if err != nil || string(output) != string(content) {
		t.Fatalf("svnlook cat: %v content=%q", err, output)
	}
	// Simulate death after svnmucc committed but before StatePublished was
	// journaled. Recovery finds the revprop and does not create r2.
	recovered, err := publisher.PublishWhale(context.Background(), &record, journal, repository, payload)
	if err != nil || recovered != revision {
		t.Fatalf("recovery revision=%d err=%v", recovered, err)
	}
	youngest, err := exec.Command(svnlook, "youngest", repository).Output()
	if err != nil || strings.TrimSpace(string(youngest)) != "1" {
		t.Fatalf("youngest=%q err=%v", youngest, err)
	}
}
