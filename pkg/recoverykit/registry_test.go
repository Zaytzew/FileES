package recoverykit

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecoveryRegistryTransitionsFromDownloadThroughGraceThenDisappears(t *testing.T) {
	now := time.Now().UTC()
	registry := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	entry := RegistryEntry{
		Schema: RegistrySchema, OperationID: uuid.NewString(), ServerID: "office", ServerName: "Office",
		KitPath: filepath.Join(t.TempDir(), "kit.fkr"), ArchiveCount: 2,
		DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour),
	}
	if err := registry.Put(entry); err != nil {
		t.Fatal(err)
	}
	if entries, err := registry.List(entry.DownloadUntil); err != nil || len(entries) != 1 {
		t.Fatalf("download expiry list=%v err=%v", entries, err)
	}
	if entries, err := registry.List(entry.AdminGraceUntil); err != nil || len(entries) != 0 {
		t.Fatalf("grace expiry list=%v err=%v", entries, err)
	}
}
