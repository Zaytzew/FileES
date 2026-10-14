package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

type importProgressDaemon struct {
	*fakeDaemon
	read func(context.Context, string) (*contract.RepoLifecycleResult, error)
}

func (d *importProgressDaemon) RepoLifecycleStatus(ctx context.Context, id string) (*contract.RepoLifecycleResult, error) {
	return d.read(ctx, id)
}

func TestDashboardImportProgressFencesAndMissingRead(t *testing.T) {
	repo := contract.RepoSummary{ID: "repo", ServerID: "server", LocalPath: "/import", LifecycleOperationID: "op"}
	for _, scenario := range []string{"progress", "completed", "failed", "missing", "read error", "wrong repo", "wrong operation", "wrong server", "no capability", "attached", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			summary := repo
			result := &contract.RepoLifecycleResult{RepoID: repo.ID, ServerID: repo.ServerID, OperationID: "op", State: "repository_created", ImportProgress: &contract.PublishProgress{FilesDone: 330, FilesTotal: 4544, BytesSent: 668280014, BytesTotal: 5838690169}}
			var readErr error
			switch scenario {
			case "completed":
				result.State = "attached"
			case "failed":
				result.LastError = "transport failed"
			case "missing":
				result.ImportProgress = nil
			case "read error":
				readErr = errors.New("unavailable")
			case "wrong repo":
				result.RepoID = "other"
			case "wrong operation":
				result.OperationID = "other"
			case "wrong server":
				result.ServerID = "other"
			case "attached":
				summary.Attached = true
			case "deleted":
				summary.ServerDeleted = true
			}
			calls := 0
			d := &importProgressDaemon{fakeDaemon: &fakeDaemon{}, read: func(_ context.Context, id string) (*contract.RepoLifecycleResult, error) {
				calls++
				if id != "op" {
					t.Fatalf("wrong requested operation: %s", id)
				}
				return result, readErr
			}}
			a := New(Config{Client: d})
			status := a.withImportProgress(context.Background(), summary, contract.RepoStatus{RepoID: repo.ID}, scenario != "no capability")
			if scenario == "progress" {
				if status.PublishProgress == nil || *status.PublishProgress != *result.ImportProgress {
					t.Fatalf("lost progress: %+v", status)
				}
				if status.PublishProgress == result.ImportProgress {
					t.Fatal("mutable result alias")
				}
			} else if status.PublishProgress != nil {
				t.Fatalf("false progress: %+v", status.PublishProgress)
			}
			if (scenario == "no capability" || scenario == "attached" || scenario == "deleted") && calls != 0 {
				t.Fatal("unnecessary IPC read")
			}
		})
	}
}

func TestDashboardRestoresImportProgressWithoutCreationDialog(t *testing.T) {
	var done atomic.Int32
	done.Store(7)
	d := &importProgressDaemon{fakeDaemon: &fakeDaemon{
		repoList: func(context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "repo", ServerID: "server", LocalPath: "/import", LifecycleOperationID: "op"}}}, nil
		},
	}, read: func(context.Context, string) (*contract.RepoLifecycleResult, error) {
		result := &contract.RepoLifecycleResult{RepoID: "repo", ServerID: "server", OperationID: "op", State: "repository_created"}
		if n := done.Load(); n >= 0 {
			result.ImportProgress = &contract.PublishProgress{FilesDone: int(n), FilesTotal: 10}
		}
		return result, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	a := New(Config{Client: d, OnChange: vc.onChange, Periodic: -1})
	go a.Run(ctx)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Repos) == 1 && vm.Repos[0].PublishProgress != nil && vm.Repos[0].PublishProgress.FilesDone == 7
	})
	done.Store(8)
	// No UI action and no full refresh: the import-only timer refreshes the row.
	vc.waitFor(t, 4*time.Second, func(vm ViewModel) bool {
		return len(vm.Repos) == 1 && vm.Repos[0].PublishProgress != nil && vm.Repos[0].PublishProgress.FilesDone == 8
	})
	done.Store(-1)
	a.Refresh()
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return len(vm.Repos) == 1 && vm.Repos[0].PublishProgress == nil })
}
