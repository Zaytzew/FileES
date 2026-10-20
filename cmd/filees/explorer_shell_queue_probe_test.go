//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"filees/pkg/cloudfiles"
	"filees/pkg/localrepo"
)

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
