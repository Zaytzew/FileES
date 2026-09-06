package contracttests

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/ipcclient"
)

func TestPassportStatusAndFaultsCrossRealIPC(t *testing.T) {
	_, rs, sock := startEventServer(t)
	cli := ipcclient.New(sock, "gui")
	summary := rs.Summary()
	issue := contract.PassportIssue{ID: "pending", Path: "Łódź.txt", Since: "2026-09-06T11:00:00Z", Phase: "locking", Code: string(errcat.CodePassportUncertain), Message: string(errcat.KeyPassportUncertain)}
	rs.SetPassportIssues([]contract.PassportIssue{issue})
	rs.SetState(contract.StateActive)
	status, err := cli.RepoStatus(t.Context(), summary.ID)
	if err != nil || len(status.PassportIssues) != 1 || status.PassportIssues[0] != issue || status.State != contract.StateInteractionRequired {
		t.Fatalf("wire=%+v %v", status, err)
	}
	issue.Phase = "checking"
	rs.SetPassportIssues([]contract.PassportIssue{issue})
	status, err = cli.RepoStatus(t.Context(), summary.ID)
	if err != nil || len(status.PassportIssues) != 1 || status.PassportIssues[0] != issue || status.State != contract.StateInteractionRequired {
		t.Fatalf("completion fence lost in IPC: %+v %v", status, err)
	}
	failure := fmt.Errorf("wrapped: %w", errcat.New(errcat.KeyPassportUncertain, nil, nil))
	rs.SetLockFuncs(func(context.Context, []string) (string, error) { return "", failure }, func(context.Context, []string) (string, error) { return "", failure })
	rs.SetPublishFunc(func(context.Context, string) (int64, error) { return 0, failure })
	path := filepath.Join(summary.LocalPath, "Łódź.txt")
	_, lockErr := cli.Lock(t.Context(), summary.ID, []string{path})
	_, unlockErr := cli.Unlock(t.Context(), summary.ID, []string{path})
	_, publishErr := cli.RepoPublish(t.Context(), summary.ID, "comment")
	for _, err := range []error{lockErr, unlockErr, publishErr} {
		var wire *ipcclient.ResponseError
		if !errors.As(err, &wire) {
			t.Fatalf("not structured: %v", err)
		}
		code, _, _, key := wire.PresentationError()
		if code != string(errcat.CodePassportUncertain) || key != string(errcat.KeyPassportUncertain) {
			t.Fatalf("fault flattened: %v", err)
		}
	}
	rs.SetPassportIssues(nil)
	status, err = cli.RepoStatus(t.Context(), summary.ID)
	if err != nil || len(status.PassportIssues) != 0 || status.State != contract.StateActive {
		t.Fatalf("recovery=%+v %v", status, err)
	}
}
