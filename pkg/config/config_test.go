package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadBatchSafetySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","max_batch_mib":256,"backlog_flush_mib":768,"shutdown_commit_timeout":"7m"}]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	repos, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].MaxBatchMiB != 256 || repos[0].BacklogFlushMiB != 768 || repos[0].ShutdownCommitTimeout != 7*time.Minute {
		t.Fatalf("decoded settings = %#v", repos[0])
	}
}

func TestLoadRejectsFlushWatermarkBelowBatchSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","max_batch_mib":512,"backlog_flush_mib":128}]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted backlog_flush_mib below max_batch_mib")
	}
}
