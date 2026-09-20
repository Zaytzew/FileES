package main

import (
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	"strings"
)

// This explains a rejected gesture using the same immutable projection as
// admission. It never grants permission or substitutes for daemon checks.
func rejectedActionReason(vm guiapp.ViewModel, request ActionRequest) string {
	kind := tray.IntentKind(request.Kind)
	// Local navigation and recovery can legitimately work without the daemon.
	remote := true
	switch kind {
	case tray.IntentOpenFolder, tray.IntentSettings, tray.IntentDownloadRecovery, tray.IntentRenameUnportable:
		remote = false
	}
	if remote {
		if !vm.Connected {
			return "action_disconnected"
		}
		if vm.Stale {
			return "action_stale"
		}
	}
	if request.RepoID != "" {
		repo, ok := projectedRepo(vm, request.RepoID)
		if !ok {
			return "action_repository_missing"
		}
		switch kind {
		case tray.IntentAttachRepository:
			if repo.State == "initializing" || repo.DisplayState() == guiapp.RepoDisplayInitializing {
				return "action_repository_initializing"
			}
		case tray.IntentLock, tray.IntentUnlock, tray.IntentPublish, tray.IntentOpenFolder:
			if !repo.Attached || strings.TrimSpace(repo.LocalPath) == "" {
				return "action_not_attached"
			}
			if kind != tray.IntentOpenFolder && !repo.CanWrite() {
				return "action_read_only"
			}
		}
	}
	// Only call these after the common connectivity check. A missing supported
	// operation is different from a repository changing while a menu was open.
	unavailable := false
	switch kind {
	case tray.IntentPairMobileDevice:
		unavailable = !vm.CanPairMobile()
	case tray.IntentRestartFileES:
		unavailable = !vm.CanRestartFileES()
	case tray.IntentShutdownFileES:
		unavailable = !vm.CanShutdownFileES()
	case tray.IntentBrowseHistory:
		unavailable = !vm.CanBrowseHistory()
	case tray.IntentLock:
		unavailable = !vm.CanMutateLock()
	case tray.IntentUnlock:
		unavailable = !vm.CanMutateUnlock()
	case tray.IntentPublish:
		unavailable = !vm.CanPublish()
	case tray.IntentAttachRepository:
		unavailable = !vm.CanAttachRepository()
	case tray.IntentManagePublicShares, tray.IntentRevokePublicShare, tray.IntentRevokePublicShares:
		unavailable = !vm.CanManagePublicShares()
	case tray.IntentAckNotice:
		unavailable = !vm.CanAckNotices()
	case tray.IntentReleaseReservation:
		unavailable = !vm.CanReleaseReservations()
	case tray.IntentRequestLockRelease:
		unavailable = !vm.CanRequestLockRelease()
	case tray.IntentAcceptLockRelease:
		unavailable = !vm.CanAcceptLockRelease()
	case tray.IntentDismissLockRelease:
		unavailable = !vm.CanDismissLockRelease()
	}
	if unavailable {
		return "action_capability_unavailable"
	}
	return "action_unavailable"
}
