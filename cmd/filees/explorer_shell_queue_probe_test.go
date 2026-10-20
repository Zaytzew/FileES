//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/cloudfiles"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

func shellProbeRootIdentity(t *testing.T, root string) (uint32, uint64) {
	t.Helper()
	wide, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		t.Fatal(err)
	}
	return info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
}

func TestShellQueueKeepWaitsForRegistrationBeforeConnect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	helper := probeTool(t, "FILEES_CFAPI")
	root := filepath.Join(t.TempDir(), "point")
	if err := os.MkdirAll(filepath.Join(root, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchorSeedPath(root), []byte(`{"schema":"filees.anchor-seed/v1","revision":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	const contents = "local bytes must survive provider repair"
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	volume, fileID := shellProbeRootIdentity(t, root)
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.BeginAnchorAttach("fixture", "repo", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApproveAttach(r.OperationID, "fixture", "repo", "svn+ssh://example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	r, err = store.MarkAttached(r.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: helper, lifecycle: store, localIntentMu: new(sync.Mutex), options: &anchorOptions{}}
	m.options.servers.Store(map[string]bool{"fixture": true})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	m.localIntentMu.Lock()
	locked := true
	go func() { defer close(done); m.keep(ctx, r) }()
	t.Cleanup(func() {
		if locked {
			m.localIntentMu.Unlock()
		}
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("keep did not drain")
			return
		}
		if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
			t.Errorf("fixture cleanup: %s %v", out, err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.shellQueue.mu.Lock()
		n := len(m.shellQueue.pending)
		m.shellQueue.mu.Unlock()
		if n != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prepare did not enqueue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	info, err := m.call(ctx, "", "info", "--root", root)
	if err != nil || !info.OK || info.Status != 0 {
		t.Fatalf("provider connected before FIFO registration: %+v %v", info, err)
	}
	m.localIntentMu.Unlock()
	locked = false
	m.reconcileShellQueue(ctx)
	deadline = time.Now().Add(5 * time.Second)
	for {
		info, err = m.call(ctx, "", "info", "--root", root)
		if err == nil && info.OK && info.Status != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider did not connect: %+v %v", info, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(shellRootsForPath(t, root)) != 1 {
		t.Fatal("connected without Shell registration")
	}
	icon, err := anchorFolderIconPath()
	if err != nil {
		t.Fatal(err)
	}
	// Even a direct caller must refuse Register while a provider is active.
	if err = m.registerShellPreference(ctx, r, icon); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal("active provider reached Register", err)
	}
	m.shellQueue.mu.Lock()
	old := m.shellQueue.providers[shellRootKey(root)]
	m.shellQueue.mu.Unlock()
	if old == nil {
		t.Fatal("missing provider lifetime lease")
	}
	if _, err = store.SetExplorerShortcut(r.OperationID, true); err != nil {
		t.Fatal(err)
	}
	m.queueShellRegistration(r)
	m.reconcileShellQueue(ctx) // EOF, full drain, status0, Register, newest preference
	select {
	case <-old.done:
	default:
		t.Fatal("registration returned before provider jobs drained")
	}
	if ids := shellRootsForPath(t, root); len(ids) != 1 || shellRootPinned(t, ids[0]) != 1 {
		t.Fatal("repair did not apply latest preference")
	}
	deadline = time.Now().Add(12 * time.Second)
	for {
		info, err = m.call(ctx, "", "info", "--root", root)
		if err == nil && info.OK && info.Ours && info.Status != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("repaired provider did not reconnect: %+v %v", info, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v, id := shellProbeRootIdentity(t, root); v != volume || id != fileID {
		t.Fatal("root replaced during repair")
	}
	if body, err := os.ReadFile(filepath.Join(root, "kept.txt")); err != nil || string(body) != contents {
		t.Fatal("bytes changed", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not drain after repair")
	}
	info, err = m.call(context.Background(), "", "info", "--root", root)
	if err != nil || !info.OK || !info.Ours || info.Status != 0 {
		t.Fatalf("metadata unhealthy after disconnect: %+v %v", info, err)
	}
	restarted := &anchorManager{helper: helper, lifecycle: store, localIntentMu: new(sync.Mutex), options: m.options}
	restartCtx, stopRestart := context.WithCancel(t.Context())
	restartDone := make(chan struct{})
	go func() { defer close(restartDone); restarted.keep(restartCtx, r) }()
	defer func() { stopRestart(); <-restartDone }()
	deadline = time.Now().Add(8 * time.Second)
	for {
		info, err = restarted.call(restartCtx, "", "info", "--root", root)
		if err == nil && info.OK && info.Ours && info.Status != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart did not connect: %+v %v", info, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopRestart()
	<-restartDone
	info, err = restarted.call(context.Background(), "", "info", "--root", root)
	if err != nil || !info.OK || !info.Ours || info.Status != 0 {
		t.Fatal("restart damaged native metadata", info, err)
	}
	if v, id := shellProbeRootIdentity(t, root); v != volume || id != fileID {
		t.Fatal("restart changed root identity")
	}
	if body, err := os.ReadFile(filepath.Join(root, "kept.txt")); err != nil || string(body) != contents {
		t.Fatal("restart changed bytes", err)
	}
}

func TestShellQueueUntrackedProviderIsNotStoppedOrRegistered(t *testing.T) {
	m, r := shellQueueUnitManager(t)
	m.helper = probeTool(t, "FILEES_CFAPI")
	if answer, err := m.call(t.Context(), "", "register", "--root", r.LocalPath, "--identity", r.ServerID+"\x1f"+r.RepoID); err != nil || !answer.OK {
		t.Fatal(answer, err)
	}
	streams, err := startAnchorHelper(t.Context(), m.helper, r.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = streams.stdin.Close()
		if err := streams.wait(); err != nil {
			t.Error(err)
		}
		if out, err := exec.Command(m.helper, "unregister", "--root", r.LocalPath).CombinedOutput(); err != nil {
			t.Errorf("fixture cleanup: %s %v", out, err)
		}
	})
	if line, err := bufio.NewReader(streams.stdout).ReadString('\n'); err != nil || !strings.Contains(line, `"connected"`) {
		t.Fatal(line, err)
	}
	m.queueShellRegistration(r)
	m.reconcileShellQueue(t.Context())
	if len(m.shellQueue.pending) != 1 || len(shellRootsForPath(t, r.LocalPath)) != 0 {
		t.Fatal("untracked active root reached registration")
	}
	answer, err := m.call(t.Context(), "", "info", "--root", r.LocalPath)
	if err != nil || !answer.OK || !answer.Ours || answer.Status == 0 {
		t.Fatal("untracked provider stopped/damaged", answer, err)
	}
	_ = streams.stdin.Close()
	if err = streams.wait(); err != nil {
		t.Fatal(err)
	}
	answer, err = m.call(t.Context(), "", "info", "--root", r.LocalPath)
	if err != nil || !answer.OK || !answer.Ours || answer.Status != 0 {
		t.Fatal("refusal damaged metadata after disconnect", answer, err)
	}
	m.reconcileShellQueue(t.Context())
	if len(m.shellQueue.pending) != 0 || len(shellRootsForPath(t, r.LocalPath)) != 1 {
		t.Fatal("safe retry after external disconnect failed")
	}
}

func TestShellQueueRepairsSkippedStartupRegistrationFIFO(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	helper := probeTool(t, "FILEES_CFAPI")
	parent := t.TempDir()
	store, err := localrepo.Open(filepath.Join(parent, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: helper, lifecycle: store, localIntentMu: new(sync.Mutex), options: &anchorOptions{}}
	m.options.servers.Store(map[string]bool{"fixture": true})
	var records []localrepo.Record
	for _, name := range []string{"A", "B"} {
		root := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(root, ".filees"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(anchorSeedPath(root), []byte(`{"schema":"filees.anchor-seed/v1","revision":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := store.BeginAnchorAttach("fixture", name, root, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.ApproveAttach(r.OperationID, "fixture", name, "svn+ssh://example/"+name, "rw"); err != nil {
			t.Fatal(err)
		}
		r, err = store.MarkAttached(r.OperationID, name)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
		t.Cleanup(func() {
			if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
				t.Errorf("fixture cleanup: %s %v", out, err)
			}
		})
	}
	m.localIntentMu.Lock()
	for _, r := range records {
		if err := m.prepare(t.Context(), r); err != nil {
			m.localIntentMu.Unlock()
			t.Fatal(err)
		}
	}
	if len(m.shellQueue.pending) != 2 {
		m.localIntentMu.Unlock()
		t.Fatal("busy preparation lost Shell registration intent")
	}
	m.reconcileShellQueue(t.Context()) // real admission is busy, preserve both
	if len(m.shellQueue.pending) != 2 {
		m.localIntentMu.Unlock()
		t.Fatal("busy head lost")
	}
	m.localIntentMu.Unlock()
	if _, err := store.SetExplorerShortcut(records[1].OperationID, true); err != nil {
		t.Fatal(err)
	}
	m.reconcileShellQueue(t.Context())
	if len(shellRootsForPath(t, records[0].LocalPath)) != 1 || len(shellRootsForPath(t, records[1].LocalPath)) != 0 {
		t.Fatal("younger root overtook FIFO head")
	}
	m.reconcileShellQueue(t.Context())
	ids := shellRootsForPath(t, records[1].LocalPath)
	if len(ids) != 1 || shellRootPinned(t, ids[0]) != 1 {
		t.Fatal("latest visibility was not applied")
	}
	for _, r := range records {
		if !cloudfiles.IsSyncRoot(r.LocalPath) {
			t.Fatal("native registration changed")
		}
	}
	// A new manager rebuilds desired registrations from persisted attachment
	// state, including already seeded roots; no extra durable queue to lose.
	restarted := &anchorManager{helper: helper, lifecycle: store, localIntentMu: m.localIntentMu, options: m.options}
	for _, r := range records {
		if err := restarted.prepare(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	for range records {
		restarted.reconcileShellQueue(t.Context())
	}
	for _, r := range records {
		if len(shellRootsForPath(t, r.LocalPath)) != 1 {
			t.Fatal("restart lost/duplicated registration")
		}
	}
	// Detach/relocation invalidate old desired registrations. Cancelling a
	// stale task must not unregister the native root or modify the files.
	for _, r := range records {
		restarted.queueShellRegistration(r)
	}
	if _, err := store.BeginDetach("fixture", "A", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRelocation("fixture", "B", filepath.Join(parent, "B-moved"), true); err != nil {
		t.Fatal(err)
	}
	for range records {
		restarted.reconcileShellQueue(t.Context())
	}
	if len(restarted.shellQueue.pending) != 0 {
		t.Fatal("obsolete location blocks FIFO")
	}
	for _, r := range records {
		if !cloudfiles.IsSyncRoot(r.LocalPath) || len(shellRootsForPath(t, r.LocalPath)) != 1 {
			t.Fatal("stale task changed existing registration")
		}
	}
}
