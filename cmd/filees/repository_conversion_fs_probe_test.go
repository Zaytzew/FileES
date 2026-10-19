//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/cloudfiles"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/privatefile"
	"filees/pkg/provisioning"
	"filees/pkg/reposupervisor"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWCConversionNativeIPCProgressAndRetry(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	ipcDir, err := os.MkdirTemp("", "fcvipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ipcDir) }) // this test's freshly allocated directory only
	if err := privatefile.Harden(ipcDir); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(ipcDir, "ipc.sock")
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: f.record.ServerID})
	server.RegisterRepoAccess(f.record.RepoID, f.record.RepoURL, f.record.LocalPath, f.record.ServerID, "rw")
	server.SetWCConversionService(f.p)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "conversion-native")
	accepted, err := cli.WCConversion(ctx, contract.WCConversionPayload{ServerID: f.record.ServerID, RepoID: f.record.RepoID, Action: "begin", SourcePath: f.record.LocalPath})
	if err != nil || accepted.OperationID == "" {
		t.Fatal(accepted, err)
	}
	var sawPreserve, sawCleanup atomic.Bool
	f.p.conversionChanged = func(string) {
		p := f.p.WCConversionStatus(f.record.ServerID, f.record.RepoID)
		if p == nil || !p.CountersKnown || p.Done < 1 {
			return
		}
		if p.Phase == "preserving" {
			sawPreserve.Store(true)
		}
		if p.Phase == "cleanup" {
			sawCleanup.Store(true)
		}
	}
	f.hook = func(label string) error {
		if strings.HasPrefix(label, "copy-ready:") {
			return errors.New("fixture copy interruption")
		}
		return nil
	}
	f.run(t, ctx)
	blocked, err := cli.RepoStatus(ctx, f.record.RepoID)
	if err != nil || blocked.Conversion == nil || !blocked.Conversion.CanRetry || blocked.Conversion.Error == "" {
		t.Fatal(blocked, err)
	}
	// Clearing the known error is explicit and bound to this same intent.
	if _, err = cli.WCConversion(ctx, contract.WCConversionPayload{ServerID: f.record.ServerID, RepoID: f.record.RepoID, Action: "retry", OperationID: accepted.OperationID}); err != nil {
		t.Fatal(err)
	}
	f.hook = nil
	f.run(t, ctx)
	f.assertComplete(t)
	final, err := cli.WCConversion(ctx, contract.WCConversionPayload{ServerID: f.record.ServerID, RepoID: f.record.RepoID, Action: "status"})
	if err != nil || final.Phase != "complete" || final.Running || final.CanRollback || final.CanRetry || final.Error != "" || !sawPreserve.Load() || !sawCleanup.Load() {
		t.Fatal(final, err, sawPreserve.Load(), sawCleanup.Load())
	}
}

func TestWCConversionNativePublicRollbackRestoresOriginal(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	f.hook = func(label string) error {
		if label == "point-ready" {
			return errors.New("fixture before preservation")
		}
		return nil
	}
	f.run(t, t.Context())
	status := f.p.WCConversionStatus(f.record.ServerID, f.record.RepoID)
	if status == nil || !status.CanRollback {
		t.Fatal(status)
	}
	if _, err := f.p.WCConversion(t.Context(), contract.WCConversionPayload{ServerID: f.record.ServerID, RepoID: f.record.RepoID, Action: "rollback", OperationID: status.OperationID}); err != nil {
		t.Fatal(err)
	}
	f.hook = nil
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if r.ConversionID != "" || r.Anchor || r.State != localrepo.StateAttached || r.LastError != "" {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(r.LocalPath, "folder", "drawing00.dwg")); err != nil {
		t.Fatal(err)
	}
	status = f.p.WCConversionStatus(r.ServerID, r.RepoID)
	if status.Phase != "rolled_back" || !status.CanBegin {
		t.Fatal(status)
	}
}

func TestWCConversionNativeResumesWithSupervisorAfterRuntimeIsGone(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	f.hook = func(label string) error {
		if label == "point-metadata-pending" {
			return errors.New("fixture restart during point initialization")
		}
		return nil
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if r.LastError == "" {
		t.Fatal("restart boundary not reached")
	}
	f.restart(t)
	if _, err := f.p.WCConversion(t.Context(), contract.WCConversionPayload{ServerID: r.ServerID, RepoID: r.RepoID, Action: "retry", OperationID: r.ConversionID}); err != nil {
		t.Fatal(err)
	}
	view := clientview.View{Generation: 3, Repositories: []clientview.Repository{{RepoID: r.RepoID, URL: r.RepoURL, Access: r.Access, State: "active"}}}
	starter := &conversionQuiesceStarter{stops: map[reposupervisor.Key]int{}}
	supervisor, _ := reposupervisor.New(starter, nil)
	if err := supervisor.Apply(t.Context(), r.ServerID, view.Generation, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); f.p.runOne(t.Context(), r.OperationID) }()
	select {
	case event := <-f.events:
		if !event.Quiesce || event.Detach || event.Conversion == nil {
			t.Fatal("missing restart fence", event)
		}
		event.Result <- quiesceProjectedRuntime(t.Context(), supervisor, event, view, map[reposupervisor.Key]repoRuntime{}, f.p.local)
	case <-time.After(30 * time.Second):
		t.Fatal("missing supervisor stop request")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("conversion did not complete after supervisor ack")
	}
	f.assertComplete(t)
}

func TestConversionInventoryUnderMetadataReadPin(t *testing.T) {
	base := t.TempDir()
	repo, wc := filepath.Join(base, "repo"), filepath.Join(base, "wc")
	probeRun(t, base, probeTool(t, "FILEES_PROBE_SVNADMIN"), "create", repo)
	probeRun(t, base, probeTool(t, "FILEES_PROBE_SVN"), "checkout", "-q", svnurl.File(repo), wc)
	name, _ := windows.UTF16PtrFromString(filepath.Join(wc, ".svn", "wc.db"))
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	cli := client.New(client.Options{NativeSVNPath: probeTool(t, "FILEES_SVN_PROBE"), Timeout: time.Minute})
	_, err = cli.(client.ConversionInventoryReader).ConversionInventory(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
}

func TestConversionRenamePinnedTree(t *testing.T) {
	for _, withChild := range []bool{false, true} {
		t.Run(fmt.Sprint(withChild), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "old")
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "sub", "file"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			var pin *conversionTreePin
			if withChild {
				var err error
				pin, err = conversionPinTree(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				file, _, err := conversionOpen(root, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
				if err != nil {
					t.Fatal(err)
				}
				pin = &conversionTreePin{Files: map[string]*os.File{".": file}}
			}
			defer pin.close()
			err := conversionRename(pin.Files["."], filepath.Join(base, "new"))
			if withChild {
				if err == nil {
					t.Fatal("expected Windows descendant pin refusal")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type conversionProbeClient struct{ anchorBatchProbeReader }

func (c conversionProbeClient) ConversionInventory(ctx context.Context, wc string) (client.ConversionInventory, error) {
	return c.Client.(client.ConversionInventoryReader).ConversionInventory(ctx, wc)
}
func (c conversionProbeClient) InspectConversionRoot(ctx context.Context, target string) (client.ConversionRootInfo, error) {
	actual := target
	if target == c.authority {
		actual = c.actual
	}
	info, err := c.Client.(client.ConversionBoundReader).InspectConversionRoot(ctx, actual)
	if err == nil && info.URL == c.actual {
		info.URL = c.authority
	}
	return info, err
}
func (c conversionProbeClient) CheckoutDepthEmptyAt(ctx context.Context, target, wc string, rev int64) (string, error) {
	return c.Client.(client.ConversionBoundReader).CheckoutDepthEmptyAt(ctx, c.target(target), wc, rev)
}
func (c conversionProbeClient) HistoryFetchTree(ctx context.Context, target string, rev int64, dest string, pairs []client.HistoryTreePair) (client.HistoryTreeReceipt, error) {
	return c.Client.(client.HistoryTreeReader).HistoryFetchTree(ctx, c.target(target), rev, dest, pairs)
}

type conversionNativeRunFixture struct {
	p         *daemonProvisioner
	record    localrepo.Record
	manager   *anchorManager
	events    chan provisionedAttachment
	directory string
	storePath string
	hook      func(string) error
	fetches   atomic.Int64
}

func newConversionNativeRunFixture(t *testing.T, counts ...int) *conversionNativeRunFixture {
	t.Helper()
	base := t.TempDir()
	repo, wc := filepath.Join(base, "repo"), filepath.Join(base, "WC-Zażółć")
	cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
	probeRun(t, base, admin, "create", repo)
	uri := svnurl.File(repo)
	probeRun(t, base, cli, "checkout", "-q", uri, wc)
	if err := os.MkdirAll(filepath.Join(wc, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	count := 12
	if len(counts) > 0 {
		count = counts[0]
	}
	for i := range count {
		if err := os.WriteFile(filepath.Join(wc, "folder", fmt.Sprintf("drawing%02d.dwg", i)), bytes.Repeat([]byte("published fixture\n"), i+1), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probeRun(t, base, cli, "add", "--force", wc)
	probeRun(t, base, cli, "propset", "svn:ignore", "*.bak\ncache\nempty-cache", wc)
	probeRun(t, base, cli, "commit", "-q", "-m", "conversion fixture", wc)
	probeRun(t, base, cli, "update", "-q", wc)
	if err := os.MkdirAll(filepath.Join(wc, "cache", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, "empty-cache"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"local.bak", "cache/nested/ignored.bin"} {
		if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(rel)), []byte("preserved "+rel), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureWorkingCopyIdentity(wc, expectedWorkingCopyIdentity("lab", "repo", "svn+ssh://fixture/repo")); err != nil {
		t.Fatal(err)
	}
	s, err := localrepo.Open(filepath.Join(base, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := s.EnsureConfiguredAttached("lab", "repo", "svn+ssh://fixture/repo", "rw", wc, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(base, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(s, journal, []clientprofile.Profile{{ServerID: "lab", ClientID: "activation"}})
	p.conversionRoot = filepath.Join(base, "conversions")
	p.anchorEnabled = func(string) bool { return true }
	p.relocationCheck = func(localrepo.Record) error { return nil }
	events := make(chan provisionedAttachment, 16)
	p.attachments = events
	native := client.New(client.Options{NativeSVNPath: probeTool(t, "FILEES_SVN_PROBE"), Timeout: time.Minute})
	mapper := conversionProbeClient{anchorBatchProbeReader{Client: native, actual: uri, authority: record.RepoURL}}
	m := &anchorManager{helper: probeTool(t, "FILEES_CFAPI"), newSVN: func(string) (client.Client, error) { return mapper, nil }}
	f := &conversionNativeRunFixture{p: p, record: record, manager: m, events: events, directory: base, storePath: filepath.Join(base, "lifecycle.json")}
	wireNativeConversion(p, m)
	factory := p.newConversionEffects
	p.newConversionEffects = func(record localrepo.Record, intent localrepo.ConversionRecord) (conversionEffectsAdapter, error) {
		adapter, err := factory(record, intent)
		if err == nil {
			adapter.(*nativeConversionEffects).checkpoint = func(label string) error {
				if strings.HasPrefix(label, "fetch-request:") {
					f.fetches.Add(1)
				}
				if f.hook != nil {
					return f.hook(label)
				}
				return nil
			}
		}
		return adapter, err
	}
	t.Cleanup(func() {
		for _, path := range []string{wc} {
			answer, _ := m.call(context.Background(), "", "info", "--root", path)
			if answer.OK && answer.Ours {
				_, _ = m.call(context.Background(), "", "unregister", "--root", path)
			}
		}
	})
	if _, err = p.queueConversion(t.Context(), record.ServerID, record.RepoID); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *conversionNativeRunFixture) run(t *testing.T, ctx context.Context) {
	t.Helper()
	daemonConversionFixture{p: f.p, r: f.record, events: f.events}.runWithin(t, ctx, 30*time.Second)
}
func (f *conversionNativeRunFixture) restart(t *testing.T) {
	t.Helper()
	s, err := localrepo.Open(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	f.p.local = s
	f.p.conversionJournals = nil
	f.hook = nil
}
func (f *conversionNativeRunFixture) assertComplete(t *testing.T) {
	t.Helper()
	if f.fetches.Load() != 0 {
		t.Fatal("conversion attempted hydration", f.fetches.Load())
	}
	r, _ := f.p.local.Get(f.record.OperationID)
	if r.State != localrepo.StateAttached || !r.Anchor || !r.Sparse || r.ConversionID != "" || r.LastError != "" {
		t.Fatalf("conversion not complete: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(r.LocalPath), "."+filepath.Base(r.LocalPath)+"-converting")); !conversionMissing(err) {
		t.Fatal("old copy not retired", err)
	}
	if entries, err := os.ReadDir(filepath.Join(r.LocalPath, "empty-cache")); err != nil || len(entries) != 0 {
		t.Fatal("empty ignored directory was not preserved", err)
	}
	for _, rel := range []string{"local.bak", "cache/nested/ignored.bin"} {
		raw, err := os.ReadFile(filepath.Join(r.LocalPath, filepath.FromSlash(rel)))
		if err != nil || string(raw) != "preserved "+rel {
			t.Fatal("ignored bytes lost", rel, string(raw), err)
		}
	}
	path := filepath.Join(r.LocalPath, "folder", "drawing00.dwg")
	info, err := os.Stat(path)
	if err != nil || !cloudfiles.IsPlaceholder(path) || !cloudfiles.RecallsOnRead(info) {
		t.Fatal("remote file was hydrated", err)
	}
	if len(f.events) != 1 {
		t.Fatal("no completed runtime publication", len(f.events))
	}
	event := <-f.events
	if !event.Repo.Anchor || !event.Repo.Sparse {
		t.Fatal(event)
	}
}

func TestConversionNativeCompleteWithIgnoredFilesAndRetirement(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	f.run(t, t.Context())
	f.assertComplete(t)
}

func TestConversionNativePreservesBuiltinIgnoredObjects(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	// Real FileES decoration is unversioned in SVN, not svn:ignore.
	if err := markManagedFolder(f.record.LocalPath, filepath.Join(f.directory, "fixture.ico")); err != nil {
		t.Fatal(err)
	}
	paths := map[string][]byte{}
	for _, rel := range []string{"desktop.ini", "folder/Thumbs.db", "folder/local.tmp", "node_modules/nested/local.bin"} {
		p := filepath.Join(f.record.LocalPath, filepath.FromSlash(rel))
		if rel != "desktop.ini" {
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("local-only "+rel), 0600); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		paths[rel] = b
	}
	if err := os.WriteFile(filepath.Join(f.record.LocalPath, "desktop.ini")+":notes", []byte("local decoration ADS"), 0600); err != nil {
		t.Fatal(err)
	}
	attrs := attributesOf(t, filepath.Join(f.record.LocalPath, "desktop.ini"))
	// Already versioned paths stay remote, even when their names match a built-in ignore.
	cli := probeTool(t, "FILEES_PROBE_SVN")
	tracked := filepath.Join(f.record.LocalPath, "tracked.bak")
	if err := os.WriteFile(tracked, []byte("published fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	probeRun(t, f.directory, cli, "add", "--no-ignore", tracked)
	probeRun(t, f.directory, cli, "commit", "-q", "-m", "versioned ignore name", f.record.LocalPath)
	probeRun(t, f.directory, cli, "update", "-q", f.record.LocalPath)
	f.run(t, t.Context())
	f.assertComplete(t)
	for rel, want := range paths {
		b, err := os.ReadFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(b, want) {
			t.Fatal("builtin ignored bytes lost", rel, err)
		}
	}
	if got := attributesOf(t, filepath.Join(f.record.LocalPath, "desktop.ini")); got != attrs {
		t.Fatal("decoration attributes changed", got, attrs)
	}
	if b, err := os.ReadFile(filepath.Join(f.record.LocalPath, "desktop.ini") + ":notes"); err != nil || string(b) != "local decoration ADS" {
		t.Fatal("decoration ADS lost", err)
	}
	if !cloudfiles.IsPlaceholder(tracked) {
		t.Fatal("versioned ignore name became a local-only copy")
	}
}

func TestConversionNativeBuiltinIgnoreStillWaitsForUnknownFile(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	if err := markManagedFolder(f.record.LocalPath, filepath.Join(f.directory, "fixture.ico")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.record.LocalPath, "not-published.txt")
	if err := os.WriteFile(p, []byte("new authored file"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	j, err := f.p.conversionJournal(r, false)
	if err != nil || j.Get().Phase != localrepo.ConversionWaiting || j.Get().Prepared {
		t.Fatal("unknown unversioned file was admitted", r, err)
	}
	if _, err := os.Stat(r.PendingLocalPath); !conversionMissing(err) {
		t.Fatal("source retained despite unknown local data", err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "new authored file" {
		t.Fatal("local data changed", err)
	}
}

func TestConversionNativeBuiltinIgnoreReceiptRecovery(t *testing.T) {
	for _, lateEdit := range []bool{false, true} {
		t.Run(fmt.Sprint("late-edit-", lateEdit), func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			p := filepath.Join(f.record.LocalPath, "desktop.ini")
			if err := os.WriteFile(p, []byte("original local decoration"), 0600); err != nil {
				t.Fatal(err)
			}
			f.hook = func(label string) error {
				if label == "copy-moved:desktop.ini" {
					return errors.New("builtin ignored fixture interruption")
				}
				return nil
			}
			f.run(t, t.Context())
			r, _ := f.p.local.Get(f.record.OperationID)
			if r.LastError == "" {
				t.Fatal("interruption boundary not reached")
			}
			if lateEdit {
				if err := os.WriteFile(p, []byte("late changed decoration"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.restart(t)
			if _, err := f.p.WCConversion(t.Context(), contract.WCConversionPayload{ServerID: f.record.ServerID, RepoID: f.record.RepoID, Action: "retry", OperationID: r.ConversionID}); err != nil {
				t.Fatal(err)
			}
			f.run(t, t.Context())
			if !lateEdit {
				f.assertComplete(t)
				if b, err := os.ReadFile(p); err != nil || string(b) != "original local decoration" {
					t.Fatal("recovered decoration lost", err)
				}
				return
			}
			r, _ = f.p.local.Get(f.record.OperationID)
			if r.LastError == "" {
				t.Fatal("late local edit not protected")
			}
			if b, err := os.ReadFile(p); err != nil || string(b) != "late changed decoration" {
				t.Fatal("late local edit lost", err)
			}
			if _, err := os.Stat(r.PendingLocalPath); err != nil {
				t.Fatal("old source lost", err)
			}
		})
	}
}

func TestConversionNativePointMetadataReceiptRecovery(t *testing.T) {
	for _, lateData := range []bool{false, true} {
		t.Run(fmt.Sprint("late-data-", lateData), func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			f.hook = func(label string) error {
				if label == "point-metadata-pending" {
					return errors.New("fixture before root metadata")
				}
				return nil
			}
			f.run(t, t.Context())
			r, _ := f.p.local.Get(f.record.OperationID)
			j, err := f.p.conversionJournal(r, false)
			if err != nil || j.Get().Phase != localrepo.ConversionCreating || r.LastError == "" {
				t.Fatal("metadata boundary not reached", r, err)
			}
			var receipt conversionPointReceipt
			if err := conversionReadJSON(filepath.Join(f.p.conversionRoot, r.ConversionID, "point.json"), &receipt); err != nil || receipt.ID == "" || !receipt.MetadataPending {
				t.Fatal("missing root custody before metadata", receipt, err)
			}
			late := filepath.Join(r.LocalPath, "late-data.txt")
			if lateData {
				if err := os.WriteFile(late, []byte("preserve new data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.restart(t)
			if _, err := f.p.WCConversion(t.Context(), contract.WCConversionPayload{ServerID: r.ServerID, RepoID: r.RepoID, Action: "retry", OperationID: r.ConversionID}); err != nil {
				t.Fatal(err)
			}
			f.run(t, t.Context())
			if !lateData {
				f.assertComplete(t)
				return
			}
			r, _ = f.p.local.Get(f.record.OperationID)
			if r.LastError == "" {
				t.Fatal("late data did not stop initialization")
			}
			if b, err := os.ReadFile(late); err != nil || string(b) != "preserve new data" {
				t.Fatal("late data lost", err)
			}
			if _, err := os.Stat(r.PendingLocalPath); err != nil {
				t.Fatal("retained source lost", err)
			}
		})
	}
}

func TestConversionNativeResumesCopyAndPartialCleanup(t *testing.T) {
	for _, boundary := range []string{"copy-ready:local.bak", "copy-moved:local.bak", "deleted:.svn/wc.db"} {
		t.Run(boundary, func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			hit := false
			f.hook = func(label string) error {
				if label == boundary {
					hit = true
					cancel()
					return context.Canceled
				}
				return nil
			}
			f.run(t, ctx)
			if !hit {
				r, _ := f.p.local.Get(f.record.OperationID)
				t.Fatal("fault boundary not reached", r)
			}
			f.restart(t)
			f.run(t, t.Context())
			f.assertComplete(t)
		})
	}
}

func TestConversionNativeRejectsLateIgnoredEditWithoutLosingSource(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	hit := false
	f.hook = func(label string) error {
		if label == "copy-moved:local.bak" {
			hit = true
			cancel()
			return context.Canceled
		}
		return nil
	}
	f.run(t, ctx)
	if !hit {
		r, _ := f.p.local.Get(f.record.OperationID)
		t.Fatal("copy not reached", r)
	}
	if err := os.WriteFile(filepath.Join(f.record.LocalPath, "local.bak"), []byte("new local bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if r.LastError == "" || r.State != localrepo.StateRelocating {
		t.Fatal("late edit was not blocked", r)
	}
	raw, err := os.ReadFile(filepath.Join(r.LocalPath, "local.bak"))
	if err != nil || string(raw) != "new local bytes" {
		t.Fatal("new bytes lost", err)
	}
	backup := filepath.Join(filepath.Dir(r.LocalPath), "."+filepath.Base(r.LocalPath)+"-converting")
	raw, err = os.ReadFile(filepath.Join(backup, "local.bak"))
	if err != nil || string(raw) != "preserved local.bak" {
		t.Fatal("original ignored bytes lost", err)
	}
}

func TestConversionNativeOpenWriterDefersBeforeEffects(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	name, _ := windows.UTF16PtrFromString(filepath.Join(f.record.LocalPath, "folder", "drawing00.dwg"))
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	windows.CloseHandle(h)
	r, _ := f.p.local.Get(f.record.OperationID)
	if r.State != localrepo.StateAttached || r.LastError != "" || r.ConversionID == "" {
		t.Fatal("open writer was not deferred", r)
	}
	<-f.events
	f.run(t, t.Context())
	f.assertComplete(t)
}

var _ conversionNativeClient = conversionProbeClient{}

func TestConversionNativeLargerBatchAndLongIgnoredPaths(t *testing.T) {
	f := newConversionNativeRunFixture(t, 128)
	rel := filepath.Join("cache", strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 100), "file.bak")
	source := filepath.Join(f.record.LocalPath, rel)
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("long local bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	f.assertComplete(t)
	raw, err := os.ReadFile(source)
	if err != nil || string(raw) != "long local bytes" {
		t.Fatal("long ignored file lost", err)
	}
}

func TestConversionNativePreservesReadonlyAndProtectedDACL(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	path := filepath.Join(f.record.LocalPath, "local.bak")
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	file, _, err := conversionOpen(path, windows.GENERIC_READ|windows.WRITE_DAC, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetKernelObjectSecurity(windows.Handle(file.Fd()), windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sd)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	wide, _ := windows.UTF16PtrFromString(path)
	if err := windows.SetFileAttributes(wide, windows.FILE_ATTRIBUTE_READONLY|windows.FILE_ATTRIBUTE_ARCHIVE); err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	f.assertComplete(t)
	attrs, err := windows.GetFileAttributes(wide)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		t.Fatal("readonly attribute lost", err)
	}
	file, _, err = conversionOpen(path, windows.GENERIC_READ, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	acl, err := conversionDACL(file)
	if err != nil || !conversionSameDACL(acl, sd.String()) {
		t.Fatal("protected permissions changed", err)
	}
}

func TestConversionNativeLateSourceObjectStopsPartialCleanup(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	hit := false
	f.hook = func(label string) error {
		if label == "deleted:.svn/wc.db" {
			hit = true
			cancel()
			return context.Canceled
		}
		return nil
	}
	f.run(t, ctx)
	if !hit {
		t.Fatal("cleanup not reached")
	}
	r, _ := f.p.local.Get(f.record.OperationID)
	late := filepath.Join(r.PendingLocalPath, "late-new.bak")
	if err := os.WriteFile(late, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.run(t, t.Context())
	r, _ = f.p.local.Get(f.record.OperationID)
	if r.LastError == "" || r.State != localrepo.StateRelocating {
		t.Fatal("unexpected late object silently retired", r)
	}
	raw, err := os.ReadFile(late)
	if err != nil || string(raw) != "must survive" {
		t.Fatal("late source object lost", err)
	}
}

func TestConversionNativeLateWriteDuringRetainGapIsPreserved(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	hit := false
	f.hook = func(label string) error {
		if label == "retain-unpinned" {
			hit = true
			return os.WriteFile(filepath.Join(f.record.LocalPath, "folder", "drawing00.dwg"), []byte("late CAD edit"), 0600)
		}
		return nil
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if !hit || r.LastError == "" || r.State != localrepo.StateRelocating {
		t.Fatal("late edit did not stop conversion", r)
	}
	raw, err := os.ReadFile(filepath.Join(r.PendingLocalPath, "folder", "drawing00.dwg"))
	if err != nil || string(raw) != "late CAD edit" {
		t.Fatal("late CAD edit lost", err)
	}
	if _, err := os.Lstat(r.LocalPath); !conversionMissing(err) {
		t.Fatal("new point created despite changed source", err)
	}
}

func TestConversionNativePreservesAlternateStreamsWithoutHydration(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	streams := map[string]string{
		"folder/drawing00.dwg:Zone.Identifier": "[ZoneTransfer]\r\nZoneId=3\r\n",
		"folder/drawing00.dwg:Zażółć":          "named unicode stream",
		"folder/drawing00.dwg:empty":           "",
		"local.bak:notes":                      "extra stream",
		"empty-cache:notes":                    "directory stream",
	}
	for rel, bytes := range streams {
		base, name, _ := strings.Cut(rel, ":")
		if err := os.WriteFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(base))+":"+name, []byte(bytes), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t, t.Context())
	f.assertComplete(t)
	for rel, bytes := range streams {
		base, name, _ := strings.Cut(rel, ":")
		raw, err := os.ReadFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(base)) + ":" + name)
		if err != nil || string(raw) != bytes {
			t.Fatal("alternate stream lost", rel, err)
		}
	}
}

func TestConversionNativePreservesAlternateStreamsInLargerBatch(t *testing.T) {
	f := newConversionNativeRunFixture(t, 70)
	for i := range 70 {
		path := filepath.Join(f.record.LocalPath, "folder", fmt.Sprintf("drawing%02d.dwg", i)) + ":Zone.Identifier"
		if err := os.WriteFile(path, []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t, t.Context())
	f.assertComplete(t)
	for i := range 70 {
		path := filepath.Join(f.record.LocalPath, "folder", fmt.Sprintf("drawing%02d.dwg", i)) + ":Zone.Identifier"
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "[ZoneTransfer]\r\nZoneId=3\r\n" {
			t.Fatal("batch lost ADS", i, err)
		}
	}
}

func TestConversionNativeStreamReceiptsResumeAfterInterruption(t *testing.T) {
	for _, boundary := range []string{"stream-ready:", "stream-moved:", "copy-moved:local.bak", "deleted:.svn/wc.db"} {
		t.Run(boundary, func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			for _, rel := range []string{"folder/drawing00.dwg", "local.bak"} {
				if err := os.WriteFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(rel))+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hit := false
			f.hook = func(label string) error {
				if strings.HasPrefix(label, boundary) {
					hit = true
					cancel()
					return context.Canceled
				}
				return nil
			}
			f.run(t, ctx)
			if !hit {
				r, _ := f.p.local.Get(f.record.OperationID)
				t.Fatal("stream interruption boundary not reached", r)
			}
			f.restart(t)
			f.run(t, t.Context())
			f.assertComplete(t)
			for _, rel := range []string{"folder/drawing00.dwg", "local.bak"} {
				raw, err := os.ReadFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(rel)) + ":Zone.Identifier")
				if err != nil || string(raw) != "[ZoneTransfer]\r\nZoneId=3\r\n" {
					t.Fatal("ADS lost after resume", rel, err)
				}
			}
		})
	}
}

func TestConversionNativeStreamChangesStopRetirement(t *testing.T) {
	for _, change := range []string{"source-gap", "target-after-copy", "late-new-source-stream", "target-before-ack"} {
		t.Run(change, func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			rel := "folder/drawing00.dwg"
			if err := os.WriteFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(rel))+":Zone.Identifier", []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hit := false
			f.hook = func(label string) error {
				if change == "source-gap" && label == "retain-unpinned" {
					hit = true
					return os.WriteFile(filepath.Join(f.record.LocalPath, filepath.FromSlash(rel))+":Zone.Identifier", []byte("late metadata"), 0600)
				}
				if change != "source-gap" && strings.HasPrefix(label, "stream-moved:") {
					hit = true
					cancel()
					return context.Canceled
				}
				return nil
			}
			f.run(t, ctx)
			if !hit {
				t.Fatal("fault boundary absent")
			}
			r, _ := f.p.local.Get(f.record.OperationID)
			if change != "source-gap" {
				path := filepath.Join(r.LocalPath, filepath.FromSlash(rel)) + ":Zone.Identifier"
				if change == "late-new-source-stream" {
					path = filepath.Join(r.PendingLocalPath, filepath.FromSlash(rel)) + ":new-stream"
				}
				if err := os.WriteFile(path, []byte("late metadata"), 0600); err != nil {
					t.Fatal(err)
				}
				f.restart(t)
				f.run(t, t.Context())
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "late metadata" {
					t.Fatal("new metadata lost", err)
				}
			}
			r, _ = f.p.local.Get(f.record.OperationID)
			if r.LastError == "" || r.State != localrepo.StateRelocating {
				t.Fatal("changed stream was silently retired", r)
			}
			if _, err := os.Stat(r.PendingLocalPath); err != nil {
				t.Fatal("source not retained", err)
			}
		})
	}
}

func TestConversionNativeRefusesStreamsOnVersionedDirectoriesBeforeRename(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	path := filepath.Join(f.record.LocalPath, "folder") + ":Zone.Identifier"
	if err := os.WriteFile(path, []byte("keep directory metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if !strings.Contains(r.LastError, "versioned directory") {
		t.Fatal(r)
	}
	if _, err := os.Stat(r.PendingLocalPath); !conversionMissing(err) {
		t.Fatal("source moved despite unsupported directory ADS")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep directory metadata" {
		t.Fatal(err)
	}
}

func TestConversionNativeAuthorityWithdrawalStopsBeforeRetirement(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	var selected string
	f.hook = func(label string) error {
		if selected == "" && strings.HasPrefix(label, "delete-intent:") {
			selected = strings.TrimPrefix(label, "delete-intent:")
			return f.p.local.ObserveRemoteDeletion(f.record.ServerID, f.record.RepoID)
		}
		return nil
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if selected == "" || !r.RemoteDeletionObserved || r.ConversionID == "" {
		t.Fatal("authority withdrawal not retained", r)
	}
	if _, err := os.Stat(filepath.Join(r.PreservedAlternatePath, filepath.FromSlash(selected))); err != nil {
		t.Fatal("retired after authority withdrawal", err)
	}
	if len(f.events) != 0 {
		t.Fatal("runtime published after authority withdrawal")
	}
}

func TestConversionNativeRollbackPreservesNewPointBytes(t *testing.T) {
	f := newConversionNativeRunFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	hit := false
	f.hook = func(label string) error {
		if label == "point-ready" {
			hit = true
			cancel()
			return context.Canceled
		}
		return nil
	}
	f.run(t, ctx)
	if !hit {
		t.Fatal("point not reached")
	}
	f.restart(t)
	if err := os.WriteFile(filepath.Join(f.record.LocalPath, "new-note.txt"), []byte("new point bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	r, _ := f.p.local.Get(f.record.OperationID)
	j, err := f.p.conversionJournal(r, false)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := f.p.Profile(r.ServerID)
	adapter, err := f.p.newConversionEffects(r, j.Get())
	if err != nil {
		t.Fatal(err)
	}
	effects := &provisionerConversionEffects{adapter, f.p, profile}
	adapter.(*nativeConversionEffects).bindConversionAdmission(func(ctx context.Context) error { _, err := effects.current(j.Get()); return err })
	done := make(chan error, 1)
	go func() {
		_, err := (&localrepo.ConversionExecutor{Journal: j, Effects: effects}).Rollback(t.Context())
		done <- err
	}()
	select {
	case event := <-f.events:
		if !event.Quiesce || event.Detach {
			t.Fatal(event)
		}
		event.Result <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("no rollback quiesce")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.p.runOne(t.Context(), r.OperationID)
	restored, _ := f.p.local.Get(r.OperationID)
	if restored.Anchor || restored.Sparse || restored.ConversionID != "" || restored.State != localrepo.StateAttached {
		t.Fatal(restored)
	}
	raw, err := os.ReadFile(filepath.Join(r.PendingLocalPath+"-new-"+r.ConversionID, "new-note.txt"))
	if err != nil || string(raw) != "new point bytes" {
		t.Fatal("new point bytes lost", err)
	}
	raw, err = os.ReadFile(filepath.Join(r.LocalPath, "folder", "drawing00.dwg"))
	if err != nil || len(raw) == 0 {
		t.Fatal("original WC not restored", err)
	}
}

type conversionChildDescription struct{ Store, Root, URI, Helper, Native, Operation, Cut string }

func TestConversionNativeProcessChild(t *testing.T) {
	path := os.Getenv("FILEES_CONVERSION_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	var d conversionChildDescription
	if err := conversionReadJSON(path, &d); err != nil {
		t.Fatal(err)
	}
	store, err := localrepo.Open(d.Store)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := store.Get(d.Operation)
	if !ok {
		t.Fatal("missing child lifecycle")
	}
	journal, err := provisioning.NewStore(filepath.Join(filepath.Dir(d.Store), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(store, journal, []clientprofile.Profile{{ServerID: r.ServerID, ClientID: "activation"}})
	p.conversionRoot = d.Root
	p.anchorEnabled = func(string) bool { return true }
	p.relocationCheck = func(localrepo.Record) error { return nil }
	events := make(chan provisionedAttachment, 16)
	p.attachments = events
	cli := client.New(client.Options{NativeSVNPath: d.Native, Timeout: time.Minute})
	mapper := conversionProbeClient{anchorBatchProbeReader{Client: cli, actual: d.URI, authority: r.RepoURL}}
	m := &anchorManager{helper: d.Helper, newSVN: func(string) (client.Client, error) { return mapper, nil }}
	wireNativeConversion(p, m)
	factory := p.newConversionEffects
	p.newConversionEffects = func(r localrepo.Record, intent localrepo.ConversionRecord) (conversionEffectsAdapter, error) {
		adapter, err := factory(r, intent)
		if err == nil {
			adapter.(*nativeConversionEffects).checkpoint = func(label string) error {
				if label == d.Cut {
					os.Exit(73)
				}
				return nil
			}
		}
		return adapter, err
	}
	daemonConversionFixture{p: p, r: r, events: events}.run(t, t.Context())
	t.Fatal("child boundary not reached")
}

func TestConversionNativeRecoversAfterRealProcessDeath(t *testing.T) {
	for _, boundary := range []string{"point-metadata-pending", "point-metadata-ready", "copy-moved:local.bak", "deleted:.svn/wc.db", "stream-ready:folder/drawing00.dwg:Zone.Identifier:$DATA", "stream-moved:folder/drawing00.dwg:Zone.Identifier:$DATA"} {
		t.Run(boundary, func(t *testing.T) {
			f := newConversionNativeRunFixture(t)
			streamPath := filepath.Join(f.record.LocalPath, "folder", "drawing00.dwg") + ":Zone.Identifier"
			if err := os.WriteFile(streamPath, []byte("preserved after real process death"), 0600); err != nil {
				t.Fatal(err)
			}
			r, _ := f.p.local.Get(f.record.OperationID)
			native := probeTool(t, "FILEES_SVN_PROBE")
			uri := svnurl.File(filepath.Join(f.directory, "repo"))
			d := conversionChildDescription{f.storePath, f.p.conversionRoot, uri, f.manager.helper, native, r.OperationID, boundary}
			path := filepath.Join(f.directory, "child.json")
			raw, _ := json.Marshal(d)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestConversionNativeProcessChild$", "-test.v")
			cmd.Env = append(os.Environ(), "FILEES_CONVERSION_CHILD="+path)
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
				t.Fatalf("child failed: %v %s", err, output)
			}
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				answer, _ := f.manager.call(t.Context(), "", "info", "--root", r.LocalPath)
				if !answer.OK || answer.Status != 1 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			f.restart(t)
			f.run(t, t.Context())
			f.assertComplete(t)
			if raw, err := os.ReadFile(streamPath); err != nil || string(raw) != "preserved after real process death" {
				t.Fatal("ADS lost after process death", err)
			}
		})
	}
}
