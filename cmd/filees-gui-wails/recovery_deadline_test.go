package main

import (
	"context"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

type deadlineCapturingClient struct{ deadline time.Time; ok bool }

func (c *deadlineCapturingClient) RecoveryDownload(ctx context.Context, payload contract.RecoveryDownloadPayload) (*contract.RecoveryDownloadResult, error) {
	c.deadline, c.ok = ctx.Deadline()
	return &contract.RecoveryDownloadResult{OperationID: payload.OperationID, Paths: []string{"archive.svndump"}}, nil
}

// pkg/ipcclient applies a 10s deadline to the entire exchange when the caller
// supplies none, and that deadline covers the response body. A recovery
// archive is a repository dump - the one that exposed this was 859 MB - so the
// call always died as "i/o timeout" with no trace in any log. The adapter must
// therefore bound the transfer itself, generously.
func TestRecoveryDownloadCarriesADeadlineThatFitsAnArchive(t *testing.T) {
	client := &deadlineCapturingClient{}
	paths, err := recoveryDownloadAdapter{client: client}.DownloadRecovery(context.Background(), "op-1", `C:\out`)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %v", paths)
	}
	if !client.ok {
		t.Fatal("the daemon was handed a context with no deadline; ipcclient would then impose its own 10s one")
	}
	if remaining := time.Until(client.deadline); remaining < time.Hour {
		t.Fatalf("deadline leaves %v; far too little for a multi-hundred-megabyte archive", remaining)
	}
}

// A caller that already chose a deadline keeps it: cancellation must still win.
func TestRecoveryDownloadDoesNotExtendACallerDeadline(t *testing.T) {
	client := &deadlineCapturingClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := (recoveryDownloadAdapter{client: client}).DownloadRecovery(ctx, "op-1", `C:\out`); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(client.deadline); remaining > 6*time.Minute {
		t.Fatalf("adapter overrode the caller's shorter deadline (%v left)", remaining)
	}
}
