package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
)

func TestReadOnlyStarterUsesDaemonLifecycleNotReconcileContext(t *testing.T) {
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	key := reposupervisor.Key{ServerID: "office", RepoID: "archive"}
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@example/archive", t.TempDir(), key.ServerID, contract.AccessReadOnly)
	fake := &updateOnlyClient{called: make(chan struct{}, 4)}
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, LocalPath: t.TempDir(), PollInterval: 10 * time.Millisecond}, state: state}}, newSVN: func(config.Repo) client.Client { return fake }}
	reconcileCtx, cancel := context.WithCancel(context.Background())
	instance, err := starter.Start(reconcileCtx, reposupervisor.Desired{Key: key, Access: "r", State: "active"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("initial update missing")
	}
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("pipeline died with reconcile context")
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type recoveryClient struct {
	client.Client
	cleanup, status, update int
	entries                 []client.StatusEntry
	statusErr               error
}

func (f *recoveryClient) Cleanup(context.Context, string) (string, error) {
	f.cleanup++
	return "", nil
}
func (f *recoveryClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	f.status++
	return f.entries, f.statusErr
}
func (f *recoveryClient) Update(context.Context, string) (string, error) { f.update++; return "", nil }

func TestReadWriteRecoveryDefersUpdateForMissingPathsOrStatusFailure(t *testing.T) {
	for _, fake := range []*recoveryClient{{entries: []client.StatusEntry{{Path: "gone", Item: "missing"}}}, {statusErr: errors.New("status failed")}} {
		wc := t.TempDir()
		if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
			t.Fatal(err)
		}
		recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, talk.With("test-recovery"))
		if fake.cleanup != 1 || fake.status != 1 || fake.update != 0 {
			t.Fatalf("calls cleanup=%d status=%d update=%d", fake.cleanup, fake.status, fake.update)
		}
	}
}

func TestReadWriteRecoveryUpdatesCleanWorkingCopy(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &recoveryClient{}
	recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, talk.With("test-recovery"))
	if fake.cleanup != 1 || fake.status != 1 || fake.update != 1 {
		t.Fatalf("calls cleanup=%d status=%d update=%d", fake.cleanup, fake.status, fake.update)
	}
}

func TestReadWriteStarterReceivesDaemonContextAndRuntime(t *testing.T) {
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	key := reposupervisor.Key{ServerID: "office", RepoID: "documents"}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@example/documents", t.TempDir(), key.ServerID, contract.AccessReadWrite)
	called := make(chan struct{}, 1)
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID}, state: state}}, newSVN: func(config.Repo) client.Client { return &updateOnlyClient{called: make(chan struct{}, 1)} }, startReadWrite: func(ctx context.Context, runtime repoRuntime, _ client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
		if ctx != daemonCtx || runtime.state != state || desired.Key != key {
			t.Fatal("read-write factory received wrong lifecycle binding")
		}
		called <- struct{}{}
		return reposupervisor.StartManaged(ctx, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil)
	}}
	reconcileCtx, cancelReconcile := context.WithCancel(context.Background())
	instance, err := starter.Start(reconcileCtx, reposupervisor.Desired{Key: key, Access: contract.AccessReadWrite, State: "active"})
	if err != nil {
		t.Fatal(err)
	}
	cancelReconcile()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("read-write factory not called")
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
