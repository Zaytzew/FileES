package ipcserver

import (
	"slices"

	contract "filees/pkg/contract/v1"
)

func passportOverlay(state string, issues []contract.PassportIssue) string {
	if len(issues) > 0 && (state == contract.StateActive || state == contract.StateOffline || state == contract.StateDegraded) {
		return contract.StateInteractionRequired
	}
	return state
}

// Stored as presentation data so repo.status cannot block on Manager's network IO.
func (rs *RepoState) SetPassportIssues(issues []contract.PassportIssue) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if slices.Equal(rs.passportIssues, issues) {
		return
	}
	old := passportOverlay(rs.state, rs.passportIssues)
	rs.passportIssues = append([]contract.PassportIssue(nil), issues...)
	if rs.server != nil {
		rs.server.Emit(rs.server.NewRepoEvent(rs.id, contract.EvRepoStateChanged, contract.RepoStateChangedPayload{OldState: old, NewState: passportOverlay(rs.state, rs.passportIssues)}))
	}
}
