package ipcserver

import (
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestPassportIssuesSurvivePollAndAreCopied(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "socket"))
	rs := s.RegisterRepoAccess("repo", "svn://fixture/repo", t.TempDir(), "server", contract.AccessReadWrite)
	issues := []contract.PassportIssue{{ID: "pending", Path: "file.txt", Code: "LOCK-2103", Message: "passport.replacement_uncertain"}}
	rs.SetPassportIssues(issues)
	issues[0].Path = "tampered"
	rs.SetState(contract.StateActive)
	rs.SetConnectivity(contract.ConnOnline)
	snap := rs.Snapshot()
	if snap.State != contract.StateInteractionRequired || snap.PassportIssues[0].Path != "file.txt" {
		t.Fatalf("snapshot=%+v", snap)
	}
	snap.PassportIssues[0].Path = "tampered again"
	if rs.Snapshot().PassportIssues[0].Path != "file.txt" {
		t.Fatal("snapshot aliases live state")
	}
	rs.SetPassportIssues(nil)
	if snap := rs.Snapshot(); snap.State != contract.StateActive || len(snap.PassportIssues) != 0 {
		t.Fatalf("recovery=%+v", snap)
	}
	rs.SetPassportIssues(issues)
	rs.SetProjectedMetadata("repo", "svn://fixture/repo", contract.AccessReadWrite, "active", "realm", "optional", false)
	if len(rs.Snapshot().PassportIssues) != 0 {
		t.Fatal("detached projection retained live issues")
	}
}
