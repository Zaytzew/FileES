//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

// Lifecycle keeps a production-shaped authority; only the isolated reader
// substitutes its file:// fixture. This does not weaken production admission.
type anchorBatchProbeReader struct {
	client.Client
	actual, authority string
}

func (r anchorBatchProbeReader) target(target string) string {
	return r.actual + strings.TrimPrefix(target, r.authority)
}
func (r anchorBatchProbeReader) Revision(ctx context.Context, target string) (int64, error) {
	return r.Client.Revision(ctx, r.target(target))
}
func (r anchorBatchProbeReader) HistoryListTree(ctx context.Context, target string, revision int64, out string) (client.HistoryTreeSummary, error) {
	return r.Client.(anchorTreeReader).HistoryListTree(ctx, r.target(target), revision, out)
}
func (r anchorBatchProbeReader) HistoryFetchFile(ctx context.Context, target string, revision int64, out string) (int64, error) {
	return r.Client.(anchorFileReader).HistoryFetchFile(ctx, r.target(target), revision, out)
}

func (r anchorBatchProbeReader) InspectPathExclusion(ctx context.Context, wc, rel string) (client.PathExclusion, error) {
	result, err := r.Client.(anchorReleaseExclusionReader).InspectPathExclusion(ctx, wc, rel)
	result.URL = r.authority + strings.TrimPrefix(result.URL, r.actual)
	result.RepositoryRootURL = r.authority + strings.TrimPrefix(result.RepositoryRootURL, r.actual)
	return result, err
}

func (r anchorBatchProbeReader) RequireRetainedPathSupport(ctx context.Context) error {
	return r.Client.(anchorReleaseExcluder).RequireRetainedPathSupport(ctx)
}

func (r anchorBatchProbeReader) ExcludeRetainedPath(ctx context.Context, wc, rel, retained string, b client.RetainedPathBinding) error {
	b.URL = r.target(b.URL)
	return r.Client.(anchorReleaseExcluder).ExcludeRetainedPath(ctx, wc, rel, retained, b)
}

func (r anchorBatchProbeReader) RequireRetainedResumeSupport(ctx context.Context) error {
	return r.Client.(anchorReleaseResumer).RequireRetainedResumeSupport(ctx)
}

func (r anchorBatchProbeReader) ResumeRetainedPath(ctx context.Context, wc, rel, retained string, b client.RetainedPathBinding) error {
	b.URL = r.target(b.URL)
	return r.Client.(anchorReleaseResumer).ResumeRetainedPath(ctx, wc, rel, retained, b)
}

func (r anchorBatchProbeReader) InspectRetainedRemote(ctx context.Context, target string) (client.RetainedRemoteIdentity, error) {
	result, err := r.Client.(anchorReleaseRemoteReader).InspectRetainedRemote(ctx, r.target(target))
	result.URL = target
	return result, err
}

// Own disposable SVN and CFAPI point only. This is a native download/consent
// regression and guarded release round-trip, not GUI/CAD reception.
func TestAnchorBatchNativeDownloadsPastAutomaticGuard(t *testing.T) {
	helper, native := probeTool(t, "FILEES_CFAPI"), probeTool(t, "FILEES_SVN_PROBE")
	helper = filepath.Clean(helper)
	cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
	t.Setenv("FILEES_CFAPI_SHELL_PREVIEW", "1")
	root := t.TempDir()
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	probeRun(t, root, admin, "create", repo)
	if err := os.MkdirAll(filepath.Join(source, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 24 {
		if err := os.WriteFile(filepath.Join(source, "folder", fmt.Sprintf("file-%02d.txt", i)), []byte("approved bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "batch fixture", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
			t.Errorf("unregister: %s %v", out, err)
		}
	})
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	authority := "svn+ssh://_filees-client@example/11111111-1111-4111-8111-111111111111"
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAnchorAttach("server", "repo", point, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "server", "repo", authority, "rw"); err != nil {
		t.Fatal(err)
	}
	record, err = store.MarkAttached(record.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New("unused")
	state := server.RegisterRepoAccess("repo", authority, point, "server", "rw")
	if err := os.Mkdir(filepath.Join(point, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	scanner, err := watcher.NewScanner(watcher.Options{WC: point, StatePath: filepath.Join(point, ".filees", "state", "manifest.json"), PublicationPending: func() bool { return commit.HasPendingFileRelease(point) }})
	if err != nil {
		t.Fatal(err)
	}
	service := &commit.Service{Cli: svn, FenceFileRelease: scanner.WithCleanFileMutation}
	service.BindWorkingCopy("repo", point)
	state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		return service.WithLocalStatus(ctx, point, paths, confirm)
	})
	state.SetAdoptFunc(func(ctx context.Context, rel string, revision int64) error {
		_, err := svn.(interface {
			UpdateAdoptPath(context.Context, string, string, int64) (string, error)
		}).UpdateAdoptPath(ctx, point, rel, revision)
		return err
	})
	state.SetFileReleaseFunc(func(ctx context.Context, rel string, recovery bool, op, finish func() error) error {
		return service.WithFileRelease(ctx, point, rel, recovery, op, finish)
	})
	m := &anchorManager{helper: helper, profiles: filepath.Join(root, "profiles.json"), lifecycle: store, repos: server.RepoState, newSVN: func(string) (client.Client, error) {
		return anchorBatchProbeReader{Client: svn, actual: uri, authority: authority}, nil
	}, log: talk.With("batch-native"), stateEvery: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	m.running = map[string]anchorConnection{anchorKey(record): {ctx: ctx, cancel: cancel, done: done}}
	go func() { m.keep(ctx, record); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 30*time.Second, "connected batch provider", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running[anchorKey(record)].bridge != nil })
	// Admit real Cloud Files subdirectories only when the opened handle is
	// bound to this exact point. Neither another registered root nor a
	// junction inside the point acquires that permission.
	guards, err := guardAnchorReleaseDirectories(filepath.Join(point, "folder"), point)
	if err != nil {
		t.Fatal("own cloud folder refused", err)
	}
	closeAnchorReleaseGuards(guards)
	foreign := filepath.Join(root, "other-provider")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	probeRun(t, root, helper, "register", "--root", foreign, "--identity", "another-fixture")
	t.Cleanup(func() { probeRun(t, root, helper, "unregister", "--root", foreign) })
	if answer, err := m.call(t.Context(), "d\t0\tchild\tchild\n", "placeholders", "--root", foreign); err != nil || !answer.OK {
		t.Fatal("foreign cloud folder fixture", answer, err)
	}
	guards, err = guardAnchorReleaseDirectories(filepath.Join(foreign, "child"), point)
	closeAnchorReleaseGuards(guards)
	if err == nil {
		t.Fatal("another registered cloud folder admitted")
	}
	link := filepath.Join(point, "redirected")
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, foreign).CombinedOutput(); err != nil {
		t.Fatalf("owned junction fixture: %s %v", out, err)
	}
	guards, err = guardAnchorReleaseDirectories(link, point)
	closeAnchorReleaseGuards(guards)
	if err == nil {
		t.Fatal("junction inside own point admitted")
	}
	adapter := anchorBatchAdapter{manager: m}
	p := contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: "folder", Action: "download"}
	plan, err := adapter.Plan(t.Context(), p)
	if err != nil || !plan.Directory || plan.Files != 24 || plan.Bytes != 24*14 {
		t.Fatal(plan, err)
	}
	// An unchanged list in a different revision is not the approved list.
	if _, err := adapter.Execute(t.Context(), contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: "wrong"}); err == nil {
		t.Fatal("unconfirmed selection executed")
	}
	result, err := adapter.Execute(t.Context(), contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: plan.Fingerprint})
	if err != nil || result.State != "attached" || result.Completed != 24 {
		t.Fatal(result, err)
	}
	for i := range 24 {
		if b, err := os.ReadFile(filepath.Join(point, "folder", fmt.Sprintf("file-%02d.txt", i))); err != nil || string(b) != "approved bytes" {
			t.Fatal(i, string(b), err)
		}
	}
	if revision, err := svn.Revision(t.Context(), uri); err != nil || revision != 1 {
		t.Fatal("download changed repository", revision, err)
	}
	m.mu.Lock()
	bridge := m.running[anchorKey(record)].bridge
	m.mu.Unlock()
	bridge.guard.mu.Lock()
	left := len(bridge.guard.explicit)
	bridge.guard.mu.Unlock()
	if left != 0 {
		t.Fatal("explicit approvals survived batch", left)
	}

	// Set local state through the production observer only after actual SVN
	// adoption. No fabricated clean marker or unguarded metadata conversion.
	cursor := ""
	if err := m.refreshLocalStates(t.Context(), record, bridge, &cursor); err != nil {
		t.Fatal(err)
	}
	p.Action = "release"
	plan, err = adapter.Plan(t.Context(), p)
	if err != nil || plan.Files != 24 {
		t.Fatal("release preview", plan, err)
	}
	result, err = adapter.Execute(t.Context(), contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: plan.Fingerprint})
	if err != nil || result.State != "attached" || result.Completed != 24 || commit.HasPendingFileRelease(point) {
		t.Fatal("release folder", result, err)
	}
	for i := range 24 {
		rel := fmt.Sprintf("folder/file-%02d.txt", i)
		s, err := m.call(t.Context(), "", "file-state", "--root", point, "--rel", rel)
		if err != nil || !s.Partial || s.Local || s.Identity != rel {
			t.Fatal("not a remote seed", rel, s, err)
		}
		if v, err := svn.(anchorReleaseExclusionReader).InspectPathExclusion(t.Context(), point, rel); err != nil || !v.Excluded || !v.Idle {
			t.Fatal("not excluded", rel, v, err)
		}
	}
	if revision, err := svn.Revision(t.Context(), uri); err != nil || revision != 1 {
		t.Fatal("release changed repository", revision, err)
	}
}
