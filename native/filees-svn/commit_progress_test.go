//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// commit --progress reports each file whose content has been sent on stderr,
// while stdout keeps exactly the one JSON receipt (feature commit_progress_v1).
// file:// moves no bytes over a network, so byte lines are the SSH path's
// business and are not required here.
func TestCommitProgressReportsEachSentFileBesideTheReceipt(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "first.txt"), "first\n")
	write(t, filepath.Join(f.wc, "second.txt"), strings.Repeat("second\n", 4096))
	f.svnRun(t, "add", "--", "first.txt", "second.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.probe, "commit", "--disposable-wc", f.wc, "-m", "progress", "--progress", "--", "first.txt", "second.txt")
	cmd.Dir = f.wc
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit --progress: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var receipt struct {
		OK       bool     `json:"ok"`
		Revision *float64 `json:"revision"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || !receipt.OK || receipt.Revision == nil {
		t.Fatalf("receipt = %q (%v)", stdout.String(), err)
	}
	if strings.Contains(stdout.String(), "filees-progress") {
		t.Fatal("progress leaked into the JSON receipt")
	}
	// Windows writes stderr in text mode (CRLF); pkg/client strips the CR.
	if got := strings.Count(strings.ReplaceAll(stderr.String(), "\r\n", "\n"), "filees-progress\tfile\n"); got != 2 {
		t.Fatalf("file progress lines = %d, want 2; stderr=%q", got, stderr.String())
	}
}

// Without --progress nothing about progress is written anywhere.
func TestCommitWithoutProgressStaysSilent(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "quiet.txt"), "quiet\n")
	f.svnRun(t, "add", "--", "quiet.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.probe, "commit", "--disposable-wc", f.wc, "-m", "quiet", "--", "quiet.txt")
	cmd.Dir = f.wc
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "filees-progress") {
		t.Fatalf("progress without --progress: %q", stderr.String())
	}
}
