// Package app contains the GUI presentation model.
// It may only import pkg/contract/v1 and pkg/ipcclient — never engine packages.
package app

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// DaemonClient is the minimal IPC surface the GUI needs.
// pkg/ipcclient.Client satisfies this interface directly.
type DaemonClient interface {
	Hello(ctx context.Context) (*contract.HelloResult, error)
	SystemStatus(ctx context.Context) (*contract.SystemStatusResult, error)
	RepoList(ctx context.Context) (*contract.RepoListResult, error)
	RepoStatus(ctx context.Context, repoID string) (*contract.RepoStatus, error)
	ErrorList(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error)
	Lock(ctx context.Context, repoID string, paths []string) (string, error)
	Unlock(ctx context.Context, repoID string, paths []string) (string, error)
	Subscribe(ctx context.Context) (<-chan contract.Event, error)
}
