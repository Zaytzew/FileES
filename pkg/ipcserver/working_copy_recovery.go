package ipcserver

import contract "filees/pkg/contract/v1"

// Store a copied in-memory observation. GUI polling does not scan SVN or WC.
func (rs *RepoState) SetWorkingCopyRecovery(status *contract.WorkingCopyRecoveryStatus) {
	var copied *contract.WorkingCopyRecoveryStatus
	if status != nil {
		value := *status
		copied = &value
	}
	rs.mu.Lock()
	changed := (rs.workingCopyRecovery == nil) != (copied == nil)
	if copied != nil && rs.workingCopyRecovery != nil {
		changed = *copied != *rs.workingCopyRecovery
	}
	rs.workingCopyRecovery = copied
	rs.mu.Unlock()
	if changed {
		rs.EmitEvent(contract.EvRepoStateChanged, nil)
	}
}
