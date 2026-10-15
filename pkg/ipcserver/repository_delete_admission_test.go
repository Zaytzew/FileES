package ipcserver

import (
	"context"
	"errors"
	"testing"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

func TestRepositoryDeleteErrorsKeepActionAndCause(t *testing.T) {
	for _, tc := range []struct {
		err error
		key string
	}{
		{errors.New("server refused operation"), "repo.delete_failed"},
		{commit.ErrLocalStatusBusy, "repo.delete_busy"},
		{context.Canceled, "repo.delete_canceled"},
		{errcat.New("repo.delete_state_changed", nil, nil), "repo.delete_state_changed"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			s := New("unused")
			stub := &lifecycleStub{detachErr: tc.err}
			s.SetRepositoryLifecycleService(stub)
			s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
			s.RegisterProjectedRepoPolicy("repo", "Docs", "svn+ssh://example/repo", "office", "rw", "active", "owner", "optional", true)
			r := s.dispatch(lifecycleRequest(contract.CmdRepoDelete, contract.RepoDetachPayload{ServerID: "office", RepoID: "repo"}))
			if r.Status != contract.StatusError || r.Error.MessageKey != tc.key || stub.deleteCalls != 1 {
				t.Fatalf("response=%+v calls=%d", r, stub.deleteCalls)
			}
			if tc.key != "repo.delete_state_changed" && r.Error.Details["detail"] != tc.err.Error() {
				t.Fatal("cause dropped")
			}
		})
	}
}

func TestRepositoryDeletionAdmissionRechecksOwner(t *testing.T) {
	s := New("unused")
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	s.RegisterProjectedRepoPolicy("repo", "Docs", "svn+ssh://example/repo", "office", "rw", "active", "owner", "optional", true)
	if err := s.ValidateRepositoryDeletion("office", "repo"); err != nil {
		t.Fatal(err)
	}
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "other", CanCreateRepositories: true})
	if err := s.ValidateRepositoryDeletion("office", "repo"); err == nil {
		t.Fatal("changed owner admitted")
	}
}
