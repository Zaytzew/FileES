//go:build native_svn_probe

package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/shout"
	"filees/pkg/watcher"
)

// Transparent process boundary fault injector. C (Windows) or installed SVN
// (OpenBSD's unchanged CLI routing) performs every actual SVN operation.
func init() {
	real := os.Getenv("FILEES_TX_TEST_REAL")
	if real == "" || len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-test.") {
		return
	}
	arm := os.Getenv("FILEES_TX_TEST_ARM")
	verb := os.Args[1]
	if verb == "--non-interactive" && len(os.Args) > 3 && os.Args[2] == "--no-auth-cache" {
		verb = os.Args[3]
	}
	if verb == "log" {
		_, receipt := os.Stat(arm + ".receipt")
		_, block := os.Stat(arm + ".block")
		if receipt == nil && block == nil {
			fmt.Fprintln(os.Stderr, "injected lookup unavailable")
			os.Exit(88)
		}
	}
	mode := ""
	if verb == "commit" {
		if b, err := os.ReadFile(arm); err == nil {
			mode = string(b)
			if err := os.Remove(arm); err != nil {
				panic(err)
			}
		}
		f, err := os.OpenFile(arm+".calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		if err := json.NewEncoder(f).Encode(os.Args[1:]); err != nil {
			panic(err)
		}
		f.Close()
	}
	if mode == "before" {
		fmt.Fprintln(os.Stderr, "injected before commit")
		os.Exit(86)
	}
	cmd := exec.Command(real, os.Args[1:]...)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		os.Stdout.Write(output.Bytes())
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		panic(err)
	}
	if mode == "after" {
		if err := os.WriteFile(arm+".receipt", output.Bytes(), 0600); err != nil {
			panic(err)
		}
		fmt.Fprintln(os.Stderr, "injected lost commit reply")
		os.Exit(87)
	}
	os.Stdout.Write(output.Bytes())
	os.Exit(0)
}

func TestDurableCommitRealRestart(t *testing.T) {
	for _, laterEdit := range []bool{false, true} {
		t.Run(fmt.Sprintf("later_edit_%v", laterEdit), func(t *testing.T) {
			root := t.TempDir()
			repo, wc, arm := filepath.Join(root, "repo"), filepath.Join(root, "wc"), filepath.Join(root, "arm")
			run := func(tool string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), tool, args...)
				cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
				b, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v %s", tool, args, err, b)
				}
				return strings.TrimSpace(string(b))
			}
			write := func(p, value string) {
				t.Helper()
				if err := os.WriteFile(p, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run("svnadmin", "create", repo)
			url := svnurl.File(repo)
			proxy, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			opts := client.Options{Timeout: 20 * time.Second}
			if runtime.GOOS == "windows" {
				helper := os.Getenv("FILEES_SVN_PROBE")
				if !filepath.IsAbs(helper) {
					t.Fatal("FILEES_SVN_PROBE required")
				}
				t.Setenv("FILEES_TX_TEST_REAL", helper)
				opts.NativeSVNPath, opts.SvnPath = proxy, filepath.Join(root, "absent-cli")
			} else {
				svn, err := exec.LookPath("svn")
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("FILEES_TX_TEST_REAL", svn)
				opts.SvnPath = proxy
			}
			t.Setenv("FILEES_TX_TEST_ARM", arm)
			c := client.New(opts)
			if _, err := c.Checkout(t.Context(), url, wc); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(wc, ".filees", "commit_cache"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(wc, "work.txt")
			write(abs, "baseline")
			if _, err := c.Add(t.Context(), wc, []string{"work.txt"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.(interface {
				CommitWithRevision(context.Context, string, string, []string, string, bool) (string, int64, error)
			}).CommitWithRevision(t.Context(), wc, url, []string{"work.txt"}, "baseline", false); err != nil {
				t.Fatal(err)
			}
			// A seed commit is setup, not one of the counted attempts below.
			if err := os.Remove(arm + ".calls"); err != nil {
				t.Fatal(err)
			}
			completed := 0
			newService := func() (*Service, *activity.Journal) {
				t.Helper()
				journal, err := activity.Open(filepath.Join(root, "activity.json"), 20)
				if err != nil {
					t.Fatal(err)
				}
				s := &Service{Cli: client.New(opts), RepoURL: url, repoID: "recovery-test", wc: wc, Activity: journal, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10}, staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json"), Emit: func(kind string, _ any) {
					if kind == contract.EvCommitCompleted {
						completed++
					}
				}}
				s.loadCache()
				return s, journal
			}
			s, _ := newService()
			write(abs, "published payload")
			s.acceptEvent(watcher.Event{Path: abs, Rel: "work.txt", Type: watcher.EntryFile, Op: watcher.Modified})
			write(arm, "after")
			write(arm+".block", "block lookup until restart")
			if _, err := s.RequestPublish(t.Context(), wc, "recover this announcement"); err == nil {
				t.Fatal("expected lost reply + lookup failure")
			}
			if got := run("svnlook", "youngest", repo); got != "2" {
				t.Fatal(got)
			}
			in, err := s.readIntent(wc)
			if err != nil || in == nil || in.Phase != "attempting" {
				t.Fatal(in, err)
			}
			marker := run("svnlook", "propget", "--revprop", "-r", "2", repo, "filees:commit-id")
			if marker != in.ID {
				t.Fatal("durable identity not on server")
			}
			s.reconcileCleanPending(t.Context(), wc)
			if len(s.staging) != 1 {
				t.Fatal("uncertain queue erased")
			}
			if laterEdit {
				write(abs, "new unsent payload")
			}
			run("svn", "mkdir", url+"/foreign", "-m", "foreign head", "--non-interactive")
			if err := os.Remove(arm + ".block"); err != nil {
				t.Fatal(err)
			}
			// Reopen both service cache and durable journal, not just execClient.
			s, journal := newService()
			if err := s.tryCommit(t.Context(), wc); err != nil {
				t.Fatal("recovery", err)
			}
			if got := run("svnlook", "youngest", repo); got != "3" {
				t.Fatal("recovery committed again", got)
			}
			calls, err := os.ReadFile(arm + ".calls")
			if err != nil || bytes.Count(calls, []byte{'\n'}) != 1 {
				t.Fatal("commit retried", string(calls), err)
			}
			if completed != 1 || !s.shoutUsed || s.publishRevision != 2 {
				t.Fatal("wrong receipt/shout", completed, s.shoutUsed, s.publishRevision)
			}
			seen, exists, err := shout.LoadLastSeen(wc)
			if err != nil || !exists || seen != 2 {
				t.Fatal("shout not durable", seen, err)
			}
			entries := journal.List()
			if len(entries) != 1 || entries[0].Stage != activity.Published || entries[0].Revision != 2 {
				t.Fatal(entries)
			}
			if (len(s.staging) == 1) != laterEdit {
				t.Fatal("wrong remaining queue", len(s.staging))
			}
			s, journal = newService()
			if found, err := s.recoverCommit(t.Context(), wc); found || err != nil {
				t.Fatal("completed receipt replayed", found, err)
			}
			if completed != 1 || len(journal.List()) != 1 {
				t.Fatal("duplicate projection")
			}
			t.Logf("real %s backend: server r2 recovered after service restart, foreign r3 ignored, one commit, one publication, durable shout, later edit retained=%v", runtime.GOOS, laterEdit)
		})
	}
}
