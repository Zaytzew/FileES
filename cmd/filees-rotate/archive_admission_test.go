package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/svnrotate"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestRotationInterruptedArchiveAdmissionRefusesBeforeEffects(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "archive-admission")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	state := repoworker.ArchiveAdmissionBinding{Schema: "filees.archive-admission/v1", RepoID: uuid.NewString(), OperationID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Revision: 1, Epoch: 1, State: "held", ChangedAt: time.Now().UTC()}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "not-created")
	if err := runRotation(svnrotate.Config{RepoPath: filepath.Join(t.TempDir(), "not-inspected"), ArchiveDir: archive}, false, true, root); !errors.Is(err, repoworker.ErrArchiveReadOnly) {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("side effect before admission", err)
	}
}
