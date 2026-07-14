package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

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

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	path := writeConfig(t, `[
		{"id":"same","repo_url":"svn://example/a","local_path":"/tmp/a","commit_interval":"1m"},
		{"id":"same","repo_url":"svn://example/b","local_path":"/tmp/b","commit_interval":"1m"}
	]`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplikat") {
		t.Fatalf("Load error = %v, want duplicate ID", err)
	}
}

func TestLoadRejectsRelativeLocalPath(t *testing.T) {
	path := writeConfig(t, `[{"id":"r","repo_url":"svn://example/r","local_path":"relative","commit_interval":"1m"}]`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "bezwzględna") {
		t.Fatalf("Load error = %v, want absolute path error", err)
	}
}

func TestLoadRejectsNestedRootsInBothOrders(t *testing.T) {
	for _, paths := range [][2]string{{"/tmp/root", "/tmp/root/child"}, {"/tmp/root/child", "/tmp/root"}} {
		data := fmt.Sprintf(`[
			{"id":"a","repo_url":"svn://example/a","local_path":%q,"commit_interval":"1m"},
			{"id":"b","repo_url":"svn://example/b","local_path":%q,"commit_interval":"1m"}
		]`, paths[0], paths[1])
		if _, err := Load(writeConfig(t, data)); err == nil || !strings.Contains(err.Error(), "nakładające się korzenie") {
			t.Fatalf("Load(%v) error = %v, want overlap", paths, err)
		}
	}
}

func TestLoadRejectsSameRootThroughSymlink(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`[
		{"id":"a","repo_url":"svn://example/a","local_path":%q,"commit_interval":"1m"},
		{"id":"b","repo_url":"svn://example/b","local_path":%q,"commit_interval":"1m"}
	]`, realRoot, alias)
	if _, err := Load(writeConfig(t, data)); err == nil || !strings.Contains(err.Error(), "nakładające się korzenie") {
		t.Fatalf("Load error = %v, want symlink overlap", err)
	}
}

func TestLoadRejectsInvalidRegexAndTiers(t *testing.T) {
	tests := []string{
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","shout_patterns":["["]}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","commit_tiers":[{"max_mb":10,"interval":"1m"},{"max_mb":5,"interval":"1m"}]}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","commit_tiers":[{"max_mb":0,"interval":"1m"},{"max_mb":5,"interval":"1m"}]}]`,
	}
	for _, data := range tests {
		if _, err := Load(writeConfig(t, data)); err == nil {
			t.Fatalf("Load accepted invalid config: %s", data)
		}
	}
}
