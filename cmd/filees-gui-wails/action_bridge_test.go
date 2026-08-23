package main

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type pendingDeleteClient struct{}

func (pendingDeleteClient) RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{State: "detached"}, nil
}

func (pendingDeleteClient) RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{State: "deleting", ServerDeleteCompleted: true, LocalCleanupCompleted: true, LastError: "recovery pending"}, nil
}

func TestRepositoryDetachAdapterAcceptsDurableServerDeletion(t *testing.T) {
	adapter := repositoryDetachAdapter{client: pendingDeleteClient{}}
	if err := adapter.DetachRepository(t.Context(), "office", "repo-1", true); err != nil {
		t.Fatalf("durable server deletion rejected while recovery is pending: %v", err)
	}
}

func TestValidateSystemLifecycleResult(t *testing.T) {
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "restart"}, "restart", nil); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := validateSystemLifecycleResult(nil, "restart", nil); err == nil {
		t.Fatal("empty result accepted")
	}
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "shutdown"}, "restart", nil); err == nil {
		t.Fatal("unexpected action accepted")
	}
	want := errors.New("ipc failed")
	if got := validateSystemLifecycleResult(nil, "restart", want); !errors.Is(got, want) {
		t.Fatalf("transport error = %v, want %v", got, want)
	}
}
