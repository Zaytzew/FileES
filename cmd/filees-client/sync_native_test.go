package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
)

// Add uses --parents and --depth empty. Committing only the leaf files
// misses the newly scheduled parents; a fake workingCopy cannot prove SVN
// accepts the resulting commit targets.
func TestSyncWorkingCopyNativeNewDirectory(t *testing.T) {
	for _, tool := range []string{"svn", "svnadmin", "svnlook"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	mustSVN(t, "svnadmin", "create", repo)
	url := svnurl.File(repo)
	mustSVN(t, "svn", "checkout", url, wc)
	dir := filepath.Join(wc, "Faktury", "2026", "10")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "invoice.pdf")
	if err := os.WriteFile(file, []byte("invoice"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "other.txt"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	svn, _ := exec.LookPath("svn")
	c := client.New(client.Options{SvnPath: svn, Timeout: time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := 0; i < 2; i++ {
		if err := syncWorkingCopy(ctx, c, wc, "synthetic nested publish"); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(mustSVN(t, "svnlook", "youngest", repo)); got != "1" {
			t.Fatalf("HEAD=%q, expected exactly one commit", got)
		}
		if got := mustSVN(t, "svn", "status", wc); strings.TrimSpace(got) != "" {
			t.Fatalf("WC is not clean: %s", got)
		}
	}
	if got := mustSVN(t, "svn", "cat", url+"/Faktury/2026/10/invoice.pdf"); got != "invoice" {
		t.Fatalf("repository bytes=%q", got)
	}
}
