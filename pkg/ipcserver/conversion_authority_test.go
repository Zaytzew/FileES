package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestConversionStartupReadinessNeverGrantsAuthority(t *testing.T) {
	s := New("unused")
	if s.ConversionAuthorityReady("lab", "repo") {
		t.Fatal("absent projection treated as ready")
	}
	repo := s.RegisterRepoAccess("repo", "svn+ssh://fixture/repo", "", "lab", "rw")
	if s.ConversionAuthorityReady("lab", "repo") {
		t.Fatal("unactivated projection treated as ready")
	}
	s.RegisterActivation(contract.ActivationStatus{ServerID: "lab"})
	// Readiness by itself grants no relocation right, even if a repository
	// has been allocated in memory before its first authoritative projection.
	if repo.ProjectedState() == "" || repo.ProjectedState() == contract.StateInitializing {
		if s.ConversionAuthorityReady("lab", "repo") {
			t.Fatal("initializing projection treated as ready")
		}
	}
	if err := s.ValidateRelocationAdmission("lab", "repo", "svn+ssh://other/repo"); err == nil {
		t.Fatal("readiness bypassed URL authority")
	}
}
