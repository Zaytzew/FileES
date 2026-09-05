package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestRemoteDeletionPresentationIsNotOffline(t *testing.T) {
	repo := RepoViewModel{State: "deleted", Connectivity: contract.ConnOffline, AttachmentPolicy: "optional", ServerDeleted: true, LocalCopyPreserved: true, LocalCopyStatus: "clean"}
	if repoIconState(repo) != IconActive || repo.DisplayState() != RepoDisplayDeleted {
		t.Fatal("old offline leaked into completed lifecycle")
	}
	for _, status := range []string{"changed", "unknown", ""} {
		repo.LocalCopyStatus = status
		if repoIconState(repo) != IconError || repo.DisplayState() != RepoDisplayAttention {
			t.Fatalf("lost local work warning: %s", status)
		}
	}
}
