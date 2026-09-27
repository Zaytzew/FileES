package commit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

type conflictRecoveryClient struct {
	*transactionFake
	conflicted bool
	resolveErr error
	statusErr  error
	resolves   int
	head       int64
	props      string
}

type artifactRetryClient struct {
	client.Client
	client.TransactionCommitter
	commitWriterReleaser
	reverter pathReverter
	fail     bool
}

func (c *artifactRetryClient) InspectCommitWriter(ctx context.Context, wc string) (string, error) {
	return c.Client.(commitWriterInspector).InspectCommitWriter(ctx, wc)
}

func (c *artifactRetryClient) Revert(ctx context.Context, wc string, paths []string) (string, error) {
	if c.fail {
		c.fail = false
		return "", errors.New("injected interruption after resolve")
	}
	return c.reverter.Revert(ctx, wc, paths)
}

func (c *conflictRecoveryClient) Status(ctx context.Context, wc string, paths []string) ([]client.StatusEntry, error) {
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	if c.conflicted {
		return []client.StatusEntry{{Path: "a.txt", Item: "conflicted", Props: c.props, Conflicted: true}}, nil
	}
	return c.transactionFake.Status(ctx, wc, paths)
}
func (c *conflictRecoveryClient) Revision(context.Context, string) (int64, error) { return c.head, nil }
func (c *conflictRecoveryClient) CommitHead(context.Context, string) (int64, error) {
	return c.head, nil
}
func (c *conflictRecoveryClient) Resolve(_ context.Context, wc string, paths []string, accept string) (string, error) {
	c.resolves++
	if c.resolveErr != nil {
		return "", c.resolveErr
	}
	if accept != "theirs-full" || len(paths) != 1 || paths[0] != "a.txt" {
		return "", errors.New("bad decision")
	}
	if err := os.WriteFile(filepath.Join(wc, "a.txt"), []byte("server"), 0600); err != nil {
		return "", err
	}
	c.conflicted = false
	c.statuses["a.txt"] = "normal"
	return "", nil
}
func conflictRecoveryFixture(t *testing.T) (*Service, *conflictRecoveryClient, string) {
	t.Helper()
	s, c, _, wc := transactionFixture(t)
	cli := &conflictRecoveryClient{transactionFake: c, conflicted: true, head: 4, props: "none"}
	s.Cli = cli
	for path, data := range map[string]string{"a.txt": "current edit", "a.txt.mine": "earlier local", "a.txt.r3": "base", "a.txt.r4": "server"} {
		if err := os.WriteFile(filepath.Join(wc, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc, Phase: "attempting", FirstRevision: 5, Paths: []string{"a.txt"}}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	return s, cli, wc
}

func TestConflictRecoveryPreservesBothLocalVariantsAndDoesNotCommit(t *testing.T) {
	s, c, wc := conflictRecoveryFixture(t)
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Choice != contract.CommitRecoveryServerCopy || len(plan.Conflicts) != 1 || !strings.HasPrefix(plan.ConflictCopy, "!kolizje/conflicted-copy-") {
		t.Fatalf("plan %+v", plan)
	}
	if c.resolves != 0 {
		t.Fatal("planning mutated WC")
	}
	if _, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"a.txt": "current edit", "a.txt.mine": "earlier local", "a.txt.r3": "base", "a.txt.r4": "server"} {
		b, err := os.ReadFile(filepath.Join(wc, filepath.FromSlash(plan.ConflictCopy), path))
		if err != nil || string(b) != want {
			t.Fatalf("backup %s %q %v", path, b, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(wc, "a.txt"))
	if string(b) != "server" {
		t.Fatal("server not accepted")
	}
	in, err := s.readIntent(wc)
	if err != nil || in.Phase != "done" || len(in.ConflictCopies) != 1 {
		t.Fatalf("audit %+v %v", in, err)
	}
	if c.mutations != 0 || c.resolves != 1 || len(s.staging) != 1 {
		t.Fatal("unexpected publication or queue mutation")
	}
}

func TestConflictRecoveryRefusalsPreserveHold(t *testing.T) {
	for _, mode := range []string{"old-client-choice", "changed-working", "changed-mine", "changed-head", "new-conflict", "expired", "backup-blocked", "resolve-failed", "status-failed", "property-conflict"} {
		t.Run(mode, func(t *testing.T) {
			s, c, wc := conflictRecoveryFixture(t)
			if mode == "new-conflict" {
				c.conflicted = false
			}
			plan, err := s.PlanCommitRecovery(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			choice := plan.Choice
			switch mode {
			case "old-client-choice":
				choice = contract.CommitRecoveryRetryQueue
			case "changed-working":
				err = os.WriteFile(filepath.Join(wc, "a.txt"), []byte("new edit"), 0600)
			case "changed-mine":
				err = os.WriteFile(filepath.Join(wc, "a.txt.mine"), []byte("new mine"), 0600)
			case "changed-head":
				c.head = 5
			case "new-conflict":
				c.conflicted = true
			case "expired":
				s.commitRecoveryPlan.expires = time.Now().Add(-time.Second)
			case "backup-blocked":
				err = os.WriteFile(filepath.Join(wc, "!kolizje"), []byte("do not overwrite"), 0600)
			case "resolve-failed":
				c.resolveErr = errors.New("resolve refused")
			case "status-failed":
				c.statusErr = errors.New("status unavailable")
			case "property-conflict":
				c.props = "conflicted"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, choice); err == nil {
				t.Fatal("unsafe decision accepted")
			}
			in, err := s.readIntent(wc)
			if err != nil || in.Phase != "attempting" || len(s.staging) != 1 || c.mutations != 0 {
				t.Fatalf("HOLD or queue lost %+v %v", in, err)
			}
			if mode != "resolve-failed" && c.resolves != 0 {
				t.Fatal("mutated before validation")
			}
		})
	}
}

func TestConflictCopyNeverOverwritesPreviousCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "copies")
	if err := os.WriteFile(src, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConflictCopy(src, "file", dst, "now"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConflictCopy(src, "file", dst, "now"); err == nil {
		t.Fatal("overwrote backup")
	}
	b, _ := os.ReadFile(filepath.Join(dst, "file"))
	if string(b) != "first" {
		t.Fatal("first copy lost")
	}
}

// Two isolated WCs, a real binary conflict and an old attempted receipt.
// FILEES_SVN_PROBE additionally exercises the production native helper.
func TestConflictRecoveryRealSVN(t *testing.T) {
	t.Run("matching-owner", func(t *testing.T) { conflictRecoveryRealSVN(t, false) })
	t.Run("legacy-mismatched-owner", func(t *testing.T) {
		if os.Getenv("FILEES_SVN_PROBE") == "" {
			t.Skip("native helper required")
		}
		conflictRecoveryRealSVN(t, true)
	})
}

func conflictRecoveryRealSVN(t *testing.T, mismatch bool) {
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " absent")
		}
	}
	run := func(bin string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", bin, args, err, out)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	a := filepath.Join(root, "author")
	b := filepath.Join(root, "reader")
	p := filepath.ToSlash(repo)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	repoURL := (&url.URL{Scheme: "file", Path: p}).String()
	run("svnadmin", "create", repo)
	run("svn", "checkout", repoURL, a)
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(a, "plan.dwg"), "base\x00")
	run("svn", "add", filepath.Join(a, "plan.dwg"))
	run("svn", "propset", "svn:mime-type", "application/octet-stream", filepath.Join(a, "plan.dwg"))
	run("svn", "commit", "-m", "base", a)
	run("svn", "checkout", repoURL, b)
	write(filepath.Join(b, "plan.dwg"), "local\x00")
	write(filepath.Join(a, "plan.dwg"), "server\x00")
	run("svn", "commit", "-m", "server", a)
	run("svn", "update", "--accept", "postpone", b)
	// Reproduce the old watcher's queue: SVN's conflict artifacts were
	// accidentally scheduled for addition before the failed publication.
	run("svn", "add", filepath.Join(b, "plan.dwg.r1"), filepath.Join(b, "plan.dwg.r2"))
	cli := client.New(client.Options{NativeSVNPath: os.Getenv("FILEES_SVN_PROBE"), Timeout: 20 * time.Second})
	s := &Service{Cli: cli, RepoURL: repoURL, wc: b, repoID: "test", staging: map[string]*stageItem{}}
	s.Cli = &artifactRetryClient{Client: cli, TransactionCommitter: cli.(client.TransactionCommitter), commitWriterReleaser: cli.(commitWriterReleaser), reverter: cli.(pathReverter), fail: true}
	if err := os.MkdirAll(filepath.Join(b, ".filees", "commit_cache"), 0700); err != nil {
		t.Fatal(err)
	}
	in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: repoURL, RepoID: "test", WC: b, Phase: "attempting", FirstRevision: 3, Paths: []string{"plan.dwg"}}
	writerID := in.ID
	if mismatch {
		old := *in
		old.ID = uuid.NewString()
		old.Phase = "done"
		writerID = old.ID
		if err := s.writeIntent(b, &old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.writeIntent(b, in); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FILEES_SVN_PROBE") != "" {
		// Simulate the durable native fence left by the old interrupted attempt.
		write(filepath.Join(b, ".svn", "filees-native-writer-v1"), "filees.native-writer/v1\n"+writerID+"\n")
	}
	if mismatch {
		previous := filepath.Join(b, ".filees", "commit_cache", "previous-transaction.json")
		proof, err := os.ReadFile(previous)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(previous); err != nil {
			t.Fatal(err)
		}
		if _, err = s.PlanCommitRecovery(t.Context()); err == nil {
			t.Fatal("native foreign owner accepted without original proof")
		}
		owner, err := cli.(commitWriterInspector).InspectCommitWriter(t.Context(), b)
		if err != nil || owner != writerID {
			t.Fatal("native fence changed on refusal", owner, err)
		}
		if err = os.WriteFile(previous, proof, 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Choice != contract.CommitRecoveryServerCopy {
		t.Fatalf("binary conflict invisible: %+v", plan)
	}
	if _, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err == nil {
		t.Fatal("injected cleanup failure did not retain HOLD")
	}
	stored, err := s.readIntent(b)
	if err != nil || stored.Phase != "attempting" || len(stored.ConflictArtifacts) != 2 || len(stored.ConflictCopies) != 1 {
		t.Fatalf("partial recovery lost audit: %+v %v", stored, err)
	}
	retry, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if retry.Choice != contract.CommitRecoveryRetryQueue {
		t.Fatalf("resolved contents need no second choice: %+v", retry)
	}
	if _, err = s.ApplyCommitRecovery(t.Context(), retry.PlanID, retry.Choice); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(b, "plan.dwg"))
	if err != nil || string(actual) != "server\x00" {
		t.Fatalf("server bytes %q %v", actual, err)
	}
	saved, err := os.ReadFile(filepath.Join(b, filepath.FromSlash(plan.ConflictCopy), "plan.dwg"))
	if err != nil || string(saved) != "local\x00" {
		t.Fatalf("local bytes %q %v", saved, err)
	}
	head, err := cli.Revision(t.Context(), repoURL)
	if err != nil || head != 2 {
		t.Fatalf("unexpected commit: %d %v", head, err)
	}
	entries, err := cli.Status(t.Context(), b, []string{"plan.dwg"})
	if err != nil || len(entries) != 1 || entries[0].Conflicted || entries[0].Item != "normal" {
		t.Fatalf("not resolved %+v %v", entries, err)
	}
	all, err := cli.Status(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range all {
		if entry.Item == "missing" {
			t.Fatalf("conflict left missing scheduled artifact: %+v", entry)
		}
	}
}
