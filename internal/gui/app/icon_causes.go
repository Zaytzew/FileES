package app

import contract "filees/pkg/contract/v1"

// IconCause is a presentation state key, not a log or an invented error code.
// It is derived from exactly the repository priority used for the tray colour.
type IconCause struct {
	ServerID string
	RepoID   string
	Reason   string
}

func (vm ViewModel) IconCauses() []IconCause {
	if !vm.Connected {
		return []IconCause{{Reason: "daemon_offline"}}
	}
	if vm.Stale {
		return []IconCause{{Reason: "refreshing"}}
	}
	if vm.Icon == IconShout {
		return []IconCause{{Reason: "announcements"}}
	}
	if vm.Icon == IconActive {
		return nil
	}
	var causes []IconCause
	for _, r := range vm.Repos {
		if repoIconState(r) != vm.Icon {
			continue
		}
		reason := "state_unknown"
		switch {
		case r.LocalCopyPreserved && r.LocalCleanupPending:
			reason = "metadata_cleanup_pending"
		case r.LocalCopyPreserved && r.LocalCopyStatus == "changed":
			reason = "preserved_copy_changed"
		case r.LocalCopyPreserved:
			reason = "preserved_copy_unknown"
		case r.Conflicts > 0:
			reason = "conflicts"
		case r.NeedsLocate():
			reason = "working_copy_missing"
		case r.State == contract.StateInteractionRequired:
			reason = "interaction_required"
		case r.State == contract.StateDegraded:
			reason = "degraded"
		case r.Connectivity == contract.ConnOffline || r.State == contract.StateOffline:
			reason = "repository_offline"
		case r.State == contract.StateRevoked:
			reason = "access_revoked"
		case r.State == contract.StateInitializing:
			reason = "initializing"
		case r.State == contract.StateBaselining:
			reason = "baselining"
		case r.State == contract.StatePaused:
			reason = "paused"
		case r.State == contract.StateStopping:
			reason = "stopping"
		case r.CurrentOp != nil:
			reason = "working"
		}
		causes = append(causes, IconCause{ServerID: r.ServerID, RepoID: r.ID, Reason: reason})
	}
	return causes
}
