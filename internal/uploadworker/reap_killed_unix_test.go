//go:build linux || openbsd

package uploadworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/avscan"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
)

type killedReaperFixture struct {
	Intake, Channels, Repos, Trash, Mucc, Look, Phase string
}

type killedReaperScanner struct{}

func (killedReaperScanner) Scan(context.Context, string) (avscan.Verdict, string, error) {
	fmt.Print("ready")
	_, err := io.Copy(io.Discard, os.Stdin)
	return avscan.Clean, "", err
}

func TestReapKilledProcessPreservesAndReconcilesProcessing(t *testing.T) {
	if path := os.Getenv("FILEES_TEST_KILLED_REAPER"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f killedReaperFixture
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		r := Reaper{Intake: intake.Store{Root: f.Intake, MaxBytes: 1024}, Channels: &channel.Store{Root: f.Channels},
			ReposRoot: f.Repos, TrashRoot: f.Trash, Publisher: Publisher{SVNMucc: f.Mucc, SVNLook: f.Look}, Scanner: avscan.Static{Verdict: avscan.Clean}}
		if f.Phase == "claimed" {
			r.Scanner = killedReaperScanner{}
		} else {
			r.Publisher.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
				if err == nil && name == f.Mucc {
					fmt.Print("ready") // actual svnmucc has finished its commit
					_, err = io.Copy(io.Discard, os.Stdin)
				}
				return out, err
			}
		}
		if _, err := r.Reap(context.Background()); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	tools := make(map[string]string)
	for _, name := range []string{"svnadmin", "svnmucc", "svnlook"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip(name + " unavailable")
		}
		tools[name] = path
	}
	for _, phase := range []string{"claimed", "committed"} {
		t.Run(phase, func(t *testing.T) {
			r, job, _ := fixture(t, avscan.Clean)
			upload, err := r.Channels.GetUpload(job.ChannelID)
			if err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(r.ReposRoot, upload.Manifest.UploadRepoID)
			if out, err := exec.Command(tools["svnadmin"], "create", repo).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			r.Publisher = Publisher{SVNMucc: tools["svnmucc"], SVNLook: tools["svnlook"]}
			path := filepath.Join(t.TempDir(), "fixture.json")
			raw, err := json.Marshal(killedReaperFixture{r.Intake.Root, r.Channels.Root, r.ReposRoot, r.TrashRoot, tools["svnmucc"], tools["svnlook"], phase})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReapKilledProcessPreservesAndReconcilesProcessing$")
			cmd.Env = append(os.Environ(), "FILEES_TEST_KILLED_REAPER="+path)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			if _, err := io.ReadFull(output, make([]byte, 5)); err != nil {
				t.Fatal("worker did not reach the requested phase", err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("worker was not killed")
			}
			if _, err := os.Stat(filepath.Join(r.Intake.Root, job.UploadID, "PROCESSING")); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(r.Intake.PayloadPath(job.UploadID))
			if err != nil || string(payload) != "wniesiony" {
				t.Fatal("killed worker lost the payload", err)
			}
			result, err := r.Reap(ctx)
			if err != nil || result != (Result{}) {
				t.Fatal("restart blindly retried PROCESSING", result, err)
			}
			check := func(args ...string) string {
				t.Helper()
				out, err := exec.CommandContext(ctx, tools["svnlook"], args...).Output()
				if err != nil {
					t.Fatal(err)
				}
				return string(out)
			}
			if phase == "claimed" {
				if got := check("youngest", repo); got != "0\n" {
					t.Fatal("unexpected commit", got)
				}
				// All fixture workers/children are now known to have finished.
				if err := r.Intake.Release(job.UploadID); err != nil {
					t.Fatal(err)
				}
				result, err := r.Reap(ctx)
				if err != nil || result.Accepted != 1 {
					t.Fatal(result, err)
				}
			} else {
				if got := check("youngest", repo); got != "1\n" {
					t.Fatal("missing commit", got)
				}
				if check("propget", "--revprop", "-r", "1", repo, "filees:upload-id") != job.UploadID {
					t.Fatal("commit identity differs")
				}
				if check("propget", "--revprop", "-r", "1", repo, "filees:upload-sha256") != job.SHA256 {
					t.Fatal("commit checksum differs")
				}
				if check("cat", repo, "Opinia-Lodz.pdf") != string(payload) {
					t.Fatal("committed payload differs")
				}
				// Existing administrative primitives; never turn a successful
				// commit into READY and never publish the same file a second time.
				if err := r.Channels.RecordAccepted(channel.Accepted{ChannelID: job.ChannelID, UploadID: job.UploadID,
					RepoPath: "Opinia-Lodz.pdf", OriginalName: job.OriginalName, Size: job.Size, SHA256: job.SHA256, Revision: 1, AcceptedAt: r.now()}); err != nil {
					t.Fatal(err)
				}
				if err := r.Intake.Remove(job.UploadID); err != nil {
					t.Fatal(err)
				}
			}
			if got := check("youngest", repo); got != "1\n" {
				t.Fatal("recovery duplicated the commit", got)
			}
			if check("cat", repo, "Opinia-Lodz.pdf") != string(payload) {
				t.Fatal("recovered data differs")
			}
			accepted, err := r.Channels.ListAccepted(job.ChannelID)
			if err != nil || len(accepted) != 1 {
				t.Fatal("arrival not reconciled", err, len(accepted))
			}
			if _, err := os.Stat(r.Intake.PayloadPath(job.UploadID)); !os.IsNotExist(err) {
				t.Fatal("resolved spool remains", err)
			}
			var bytes, files int64
			if err := filepath.Walk(r.Channels.Root, func(_ string, info os.FileInfo, err error) error {
				if err == nil && info.Mode().IsRegular() {
					bytes += info.Size()
					files++
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			t.Logf("one reconciled upload: channel metadata %d files, %d logical bytes (not a retention bound)", files, bytes)
		})
	}
}
