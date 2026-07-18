package main

import (
	"context"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
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
