//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func init() {
	if marker := os.Getenv("FILEES_TEST_ROOT_BOUNDARY_PROCESS"); marker != "" && len(os.Args) > 1 {
		if os.Args[1] == "info" {
			fmt.Print(`{"ok":false,"error":"sync_root_boundary","hresult":"0x80070166"}`)
			os.Exit(1)
		}
		_ = os.WriteFile(marker, []byte(os.Args[1]), 0600)
		fmt.Print(`{"ok":true}`)
		os.Exit(0)
	}
}

func TestAnchorPrepareDoesNotRegisterAnUnverifiableBoundary(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mutation-attempted")
	t.Setenv("FILEES_TEST_ROOT_BOUNDARY_PROCESS", marker)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: exe}
	r := localrepo.Record{ServerID: "server", RepoID: "repo", LocalPath: t.TempDir()}
	if err := m.prepare(t.Context(), r); err == nil || !strings.Contains(err.Error(), "0x80070166") {
		t.Fatalf("boundary did not refuse with original diagnostic: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverifiable root was mutated: %v", err)
	}
}

func TestAnchorRuntimeFailurePersistsThroughRetriesAndLateProjection(t *testing.T) {
	s := ipcserver.New("unused")
	r := localrepo.Record{ServerID: "server", RepoID: "repo", LocalPath: t.TempDir()}
	m := &anchorManager{repos: s.RepoState}
	m.setRuntime(r, "failed", "register_sync_root 0x80070178")
	before := *m.runtimeOf(anchorKey(r))
	state := s.RegisterRepoAccess(r.RepoID, "svn://fixture/repo", r.LocalPath, r.ServerID, "rw")
	for _, phase := range []string{"preparing", "connecting", "failed"} {
		detail := ""
		if phase == "failed" {
			detail = before.Detail
		}
		m.setRuntime(r, phase, detail)
		got := state.Snapshot().AnchorRuntime
		if got == nil || *got != before {
			t.Fatalf("retry hid failure: %+v", got)
		}
		got.Phase = "mutated"
		if m.runtimeOf(anchorKey(r)).Phase != "failed" {
			t.Fatal("snapshot mutated source")
		}
	}
	m.setRuntime(r, "ready", "")
	if got := state.Snapshot().AnchorRuntime; got == nil || got.Phase != "ready" || got.Detail != "" {
		t.Fatal(got)
	}
	m.clearRuntime(r)
	if state.Snapshot().AnchorRuntime != nil {
		t.Fatal("retired provider still projected")
	}
}

type anchorRuntimeSink struct{}

func (anchorRuntimeSink) Write(p []byte) (int, error) { return len(p), nil }
func (anchorRuntimeSink) Close() error                { return nil }

func TestAnchorRuntimeRetainsConcreteNativeConnectionFailure(t *testing.T) {
	streams := &anchorHelperStreams{stdout: strings.NewReader(`{"ok":false,"error":"connect_sync_root","hresult":"0x800701AA"}` + "\n"), stdin: anchorRuntimeSink{}, wait: func() error { return errors.New("exit status 1") }}
	bridge := &anchorBridge{}
	err := bridge.serve(context.Background(), streams)
	if !errors.Is(err, errAnchorHelperGone) || !strings.Contains(err.Error(), "connect_sync_root (0x800701AA)") {
		t.Fatal(err)
	}
}
