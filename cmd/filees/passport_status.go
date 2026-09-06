package main

import (
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
)

func passportPendingObserver(state *ipcserver.RepoState, repo config.Repo) func([]passport.PendingStatus) {
	return func(pending []passport.PendingStatus) {
		issues := make([]contract.PassportIssue, 0, len(pending))
		for _, item := range pending {
			issues = append(issues, contract.PassportIssue{ID: "passport:" + repo.ServerID + ":" + repo.ID + ":" + item.ID, Path: item.Path, Since: item.Since, Phase: item.Phase, Code: string(errcat.CodePassportUncertain), Message: string(errcat.KeyPassportUncertain)})
		}
		state.SetPassportIssues(issues)
	}
}
