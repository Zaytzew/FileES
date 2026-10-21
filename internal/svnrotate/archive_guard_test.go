package svnrotate

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveGenerationOperationsRefuseBeforeSideEffects(t *testing.T) {
	repo, archive := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(filepath.Join(repo, "hooks", ".filees-archive-protection"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Rotate(Config{RepoPath: repo, ArchiveDir: archive}, "test", io.Discard); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatal(err)
	}
	if _, err := LoadGeneration(LoadConfig{RepoPath: repo, ArchiveDir: archive}, strings.NewReader("unused"), "test", io.Discard); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatal(err)
	}
	if _, _, err := Recover(repo, archive, ""); err == nil {
		t.Fatal("recovery admitted archive")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("created archive storage", err)
	}
	r := swapRecord{Repo: filepath.Join(t.TempDir(), "missing-hot"), Work: filepath.Join(t.TempDir(), ".load-work-test"), Meta: Meta{ArchiveDir: repo}}
	if err := r.finish(archive); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatal("recovery omitted archived generation", err)
	}
}
