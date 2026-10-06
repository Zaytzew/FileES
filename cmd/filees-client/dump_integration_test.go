package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"

	"github.com/google/uuid"
)

// The local transport models a lost LOAD receipt after the server has
// atomically installed a new FSFS generation. It exercises the CLI's durable
// create and replay path without requiring an SSH service in the test.
func TestDumpLoadResumesSameRepositoryAfterLostReceipt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows SVN in this environment rejects absolute checkout targets")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin unavailable")
	}
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn unavailable")
	}
	root := t.TempDir()
	seedRepo := filepath.Join(root, "seed")
	seedWC := filepath.Join(root, "seed-wc")
	mustSVN(t, "svnadmin", "create", seedRepo)
	mustSVN(t, "svn", "checkout", "file://"+seedRepo, seedWC)
	if err := os.WriteFile(filepath.Join(seedWC, "history.txt"), []byte("restored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSVN(t, "svn", "add", filepath.Join(seedWC, "history.txt"))
	mustSVN(t, "svn", "commit", "-m", "seed", seedWC)
	dump, err := exec.Command("svnadmin", "dump", "--quiet", seedRepo).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.dump")
	if err := os.WriteFile(source, dump, 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	opt := dumpFlags{source: source, state: state, target: filepath.Join(root, "new-wc"),
		profile: filepath.Join(root, "profile.json"), name: "Restored"}
	job, err := loadOrCreateDumpJob(opt)
	if err != nil {
		t.Fatal(err)
	}
	profile := clientprofile.Profile{ServerID: "synthetic", ClientID: uuid.NewString()}
	transport := &dumpFakeControl{t: t, repo: filepath.Join(root, "new-repo"), dump: source, lostLoad: true}
	svnClient := client.New(client.Options{SvnPath: mustLookPath(t, "svn"), Timeout: time.Minute})
	if err := runDumpLoad(context.Background(), opt, profile, svnClient, transport, &job); err == nil {
		t.Fatal("lost LOAD response was treated as success")
	}
	if _, err := os.Stat(opt.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new WC appeared before LOAD receipt: %v", err)
	}
	job, err = loadOrCreateDumpJob(opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDumpLoad(context.Background(), opt, profile, svnClient, transport, &job); err != nil {
		t.Fatal(err)
	}
	if transport.created != 1 || transport.loaded != 1 || !job.Complete {
		t.Fatalf("created=%d loaded=%d job=%+v", transport.created, transport.loaded, job)
	}
	content, err := os.ReadFile(filepath.Join(opt.target, "history.txt"))
	if err != nil || string(content) != "restored\n" {
		t.Fatalf("imported WC content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(opt.target, "dump.svndump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("carrier leaked into imported WC")
	}
	if head := strings.TrimSpace(mustSVN(t, "svnlook", "youngest", transport.repo)); head != "1" {
		t.Fatalf("new generation HEAD=%s", head)
	}
	// Simulate a crash after checkout verification but before publishing the
	// staged WC. Resume must adopt it without trying a second checkout.
	stage := opt.target + ".filees-new-" + job.OperationID
	if err := os.Rename(opt.target, stage); err != nil {
		t.Fatal(err)
	}
	job.Complete = false
	if err := saveDumpJob(opt.state, job); err != nil {
		t.Fatal(err)
	}
	if err := runDumpLoad(context.Background(), opt, profile, svnClient, transport, &job); err != nil {
		t.Fatal(err)
	}
	if !job.Complete || transport.created != 1 || transport.loaded != 1 {
		t.Fatalf("staged resume: created=%d loaded=%d job=%+v", transport.created, transport.loaded, job)
	}
	if _, err := os.Stat(filepath.Join(opt.target, "history.txt")); err != nil {
		t.Fatal(err)
	}
}

type dumpFakeControl struct {
	t               *testing.T
	repo, dump      string
	created, loaded int
	lostLoad        bool
	loadResult      control.Result
}

func (f *dumpFakeControl) Exchange(_ context.Context, ticket control.Ticket) (control.Result, error) {
	f.t.Helper()
	var payload any
	switch ticket.Type {
	case control.TicketStoragePreflight:
		payload = control.StoragePreflightResult{AvailableBytes: 1 << 30, RequiredBytes: 64 << 20}
	case control.TicketCreateRepository:
		f.created++
		mustSVN(f.t, "svnadmin", "create", f.repo)
		payload = control.CreateRepositoryResult{RepoID: "33333333-3333-4333-8333-333333333333", RepoURL: "file://" + f.repo}
	case control.TicketInitialCommit:
		payload = control.InitialCommitResult{Acknowledged: true}
	case control.TicketLoadRepositoryDump:
		if f.loadResult.Schema != "" {
			return f.loadResult, nil
		}
		f.loaded++
		oldUUID := strings.TrimSpace(mustSVN(f.t, "svnlook", "uuid", f.repo))
		newRepo := f.repo + "-generation"
		mustSVN(f.t, "svnadmin", "create", newRepo)
		bytes, err := os.ReadFile(f.dump)
		if err != nil {
			f.t.Fatal(err)
		}
		load := exec.Command("svnadmin", "load", "--quiet", newRepo)
		load.Stdin = strings.NewReader(string(bytes))
		if out, err := load.CombinedOutput(); err != nil {
			f.t.Fatalf("svnadmin load: %v %s", err, out)
		}
		if err := os.Rename(f.repo, f.repo+"-carrier"); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Rename(newRepo, f.repo); err != nil {
			f.t.Fatal(err)
		}
		newUUID := strings.TrimSpace(mustSVN(f.t, "svnlook", "uuid", f.repo))
		payload = control.LoadRepositoryDumpResult{RepoID: "33333333-3333-4333-8333-333333333333",
			OldUUID: oldUUID, NewUUID: newUUID, SourceRevisionRange: "r1:r1",
			ToolVersions: map[string]string{"svnadmin": "synthetic", "svnlook": "synthetic"}}
	default:
		f.t.Fatalf("unexpected ticket %s", ticket.Type)
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, payload, time.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	if ticket.Type == control.TicketLoadRepositoryDump {
		f.loadResult = result
		if f.lostLoad {
			f.lostLoad = false
			return control.Result{}, errors.New("receipt lost after server swap")
		}
	}
	return result, nil
}

func mustSVN(t *testing.T, program string, args ...string) string {
	t.Helper()
	out, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v %s", program, args, err, out)
	}
	return string(out)
}

func mustLookPath(t *testing.T, program string) string {
	t.Helper()
	path, err := exec.LookPath(program)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
