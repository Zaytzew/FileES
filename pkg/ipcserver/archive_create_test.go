package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

type archiveLifecycleStub struct {
	lifecycleStub
	archivePath string
}

func (s *archiveLifecycleStub) BeginCreateArchive(server, name, target, source string) (contract.RepoLifecycleResult, error) {
	s.archivePath = source
	return contract.RepoLifecycleResult{OperationID: "archive-op", ServerID: server, LocalPath: target, State: "request_pending"}, nil
}

func TestArchiveCreateRequiresExtensionAndCreationAuthority(t *testing.T) {
	for _, tc := range []struct {
		name             string
		capable, allowed bool
	}{{"old", false, true}, {"forbidden", true, false}, {"accepted", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("unused")
			legacy, archive := &lifecycleStub{}, &archiveLifecycleStub{}
			if tc.capable {
				s.SetRepositoryLifecycleService(archive)
			} else {
				s.SetRepositoryLifecycleService(legacy)
			}
			s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: tc.allowed})
			response := s.dispatch(lifecycleRequest(contract.CmdRepoCreateRequest, contract.RepoCreateRequestPayload{ServerID: "office", DisplayName: "Project", LocalPath: "/new", ArchivePath: "/external/backup.dmp"}))
			if (response.Status == contract.StatusOK) != (tc.capable && tc.allowed) {
				t.Fatal(response)
			}
			if legacy.createCalls != 0 || archive.createCalls != 0 {
				t.Fatal("archive silently downgraded to plain create")
			}
			if (archive.archivePath != "") != (tc.capable && tc.allowed) {
				t.Fatal("archive bypassed authority")
			}
			found := false
			for _, cap := range s.capabilities() {
				if cap == contract.CapRepoCreateArchive {
					found = true
				}
			}
			if found != tc.capable {
				t.Fatal("extension incorrectly advertised")
			}
		})
	}
}
