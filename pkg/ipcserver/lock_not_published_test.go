package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

// Borrowing a file that is new in the working copy failed with the generic
// LOCK-2001 and Subversion's raw E155010 text ("The node ... was not found";
// owner's report, 2026-09-24). The file simply is not on the server yet: the
// answer names it and says when borrowing becomes possible.
func TestLockOfAnUnpublishedFileSaysWhenItBecomesPossible(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	wc := t.TempDir()
	rs := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", wc, "office", contract.AccessReadWrite)
	target := filepath.Join(wc, "MB.jpg")
	rs.SetLockFuncs(func(context.Context, []string) (string, error) {
		return "", errors.New(`native SVN "lock": E155010: The node '` + target + `' was not found.; process: exit status 1`)
	}, func(context.Context, []string) (string, error) { return "", nil })

	payload, err := json.Marshal(contract.RepoLockPayload{Paths: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	resp := server.handleRepoLockUnlock(contract.Request{RequestID: "req", RepoID: "docs", Payload: payload}, true)
	if resp.Error == nil || resp.Error.Code != "LOCK-2003" || resp.Error.MessageKey != "lock.not_published" {
		t.Fatalf("unpublished lock = %+v, want LOCK-2003 lock.not_published", resp.Error)
	}
	if resp.Error.Details["path"] != "MB.jpg" {
		t.Fatalf("details = %v, want the file name", resp.Error.Details)
	}
	if strings.Contains(resp.Error.Details["detail"], "E155010") {
		t.Fatal("Subversion's raw text still reaches the user")
	}
}
