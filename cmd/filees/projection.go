package main

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

// unprojectedLocalKeys returns attachments for serverID whose repo ID is not
// (yet) present in the server's projected view. A repository this daemon
// instance just created and attached is authoritative local knowledge the
// moment CREATE_REPOSITORY/INITIAL_COMMIT succeed; the server's projected
// view only catches up on its own poll interval. Without this fallback,
// treating "absent from this snapshot" as "does not exist" both stops the
// supervisor from starting the pipeline immediately and — via
// ReconcileProjectedRepos — deletes the freshly registered RepoState from
// the IPC registry, hiding a fully working repository from repo.list/GUI
// until the next projection refresh (or a full daemon restart) rediscovers it.
func unprojectedLocalKeys(serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime) []reposupervisor.Key {
	known := make(map[string]bool, len(view.Repositories))
	for _, repo := range view.Repositories {
		known[repo.RepoID] = true
	}
	var extra []reposupervisor.Key
	for key := range attachments {
		if key.ServerID == serverID && !known[key.RepoID] {
			extra = append(extra, key)
		}
	}
	return extra
}

// attachedProjection converts server authority into supervisor input. Entries
// without a local attachment remain visible control-plane knowledge, but do
// not start a data pipeline until the user chooses a local working copy.
func attachedProjection(serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime) []reposupervisor.Desired {
	desired := make([]reposupervisor.Desired, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		if _, attached := attachments[key]; !attached {
			continue
		}
		desired = append(desired, reposupervisor.Desired{Key: key, Access: repo.Access, State: repo.State, URL: repo.URL, DisplayName: repo.DisplayName, EditingPolicy: repo.EditingPolicy, SessionTimeout: attachments[key].config.SessionTimeout})
	}
	for _, key := range unprojectedLocalKeys(serverID, view, attachments) {
		repo := attachments[key].config
		// No projection entry means the server has no opinion on this
		// repository yet, so it keeps the default policy. The barrier itself
		// is versioned in the repository, so a repository that really does
		// require locks still presents read-only files here.
		desired = append(desired, reposupervisor.Desired{Key: key, Access: repo.Access, State: "active", URL: repo.RepoURL, DisplayName: repo.ID, SessionTimeout: repo.SessionTimeout})
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i].Key.String() < desired[j].Key.String() })
	return desired
}

// applyRealmOwnership stamps the client's own RealmID and each repo's
// OwnerRealmID onto the matching runtime's config.Repo, for the autolock
// priority check in pkg/commit (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md).
// config.Repo is stored by value in the runtimes map, so an already-running
// repo instance (built from an earlier copy) only picks up a changed
// OwnerRealmID on its next restart - an accepted V1 simplification, since
// ownership transfer is a rare administrative action, not a live event.
func applyRealmOwnership(serverID string, view clientview.View, runtimes map[reposupervisor.Key]repoRuntime) {
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		entry, ok := runtimes[key]
		if !ok {
			continue
		}
		entry.config.RealmID = view.RealmID
		entry.config.OwnerRealmID = repo.OwnerRealmID
		runtimes[key] = entry
	}
}

func syncProjectionKnowledge(ipc *ipcserver.Server, serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime, lifecycle *localrepo.Store) {
	applyRealmOwnership(serverID, view, attachments)
	if ipc == nil {
		return
	}
	ipc.SetLockReleaseProjection(serverID, projectLockReleaseRequests(serverID, view.LockReleaseRequests))
	pendingCreates := pendingRepositoryCreations(serverID, lifecycle)
	projected := make([]ipcserver.ProjectedRepo, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		_, attached := attachments[key]
		state := repo.State
		pendingPath := ""
		if record, pending := pendingCreates[repo.RepoID]; pending && !attached {
			pendingPath = record.LocalPath
			if record.LastError != "" {
				state = contract.StateInteractionRequired
			} else {
				state = contract.StateInitializing
			}
		}
		projected = append(projected, ipcserver.ProjectedRepo{ID: repo.RepoID, DisplayName: repo.DisplayName, URL: repo.URL, Access: repo.Access, State: state, OwnerRealmID: repo.OwnerRealmID, AttachmentPolicy: repo.AttachmentPolicy, EditingPolicy: repo.EditingPolicy, Purpose: repo.Purpose, Attached: attached, PendingLocalPath: pendingPath})
	}
	for _, key := range unprojectedLocalKeys(serverID, view, attachments) {
		repo := attachments[key].config
		projected = append(projected, ipcserver.ProjectedRepo{ID: key.RepoID, DisplayName: repo.ID, URL: repo.RepoURL, Access: repo.Access, State: "active", Attached: true})
	}
	known := make(map[string]bool, len(projected))
	for _, repo := range projected {
		known[repo.ID] = true
	}
	if lifecycle != nil {
		now := time.Now().UTC()
		for _, record := range lifecycle.List() {
			if record.ServerID != serverID || !record.ServerDeleteCompleted || known[record.RepoID] {
				continue
			}
			retainUntil, retainErr := time.Parse(time.RFC3339Nano, record.RetainUntil)
			cleanupPending := record.State == localrepo.StateDeleting && !record.LocalCleanupCompleted
			if !cleanupPending && (retainErr != nil || !now.Before(retainUntil)) {
				continue
			}
			name := deletedRepositoryName(record)
			projected = append(projected, ipcserver.ProjectedRepo{
				ID: record.RepoID, DisplayName: name, State: "deleted", OwnerRealmID: view.RealmID,
				Attached: false, PendingLocalPath: record.LocalPath, ServerDeleted: true,
				LocalCleanupPending: cleanupPending, RetainUntil: record.RetainUntil,
				RecoveryOperationID: record.DetachOperationID,
				RecoveryAvailable:   record.RecoveryPrepared && record.RecoveryKitPath != "" && retainErr == nil && now.Before(retainUntil),
				RecoveryPending:     !record.RecoveryPrepared && retainErr == nil && now.Before(retainUntil),
				CleanupError:        record.LastError,
			})
		}
	}
	ipc.ReconcileProjectedRepos(serverID, projected)
	ready, pending := repositoryReadiness(serverID, view, attachments)
	ipc.SetActivationRepositoryReadiness(serverID, ready, pending)
}

func projectLockReleaseRequests(serverID string, requests []clientview.LockReleaseRequest) []contract.LockReleaseRequest {
	projected := make([]contract.LockReleaseRequest, 0, len(requests))
	for _, request := range requests {
		projected = append(projected, contract.LockReleaseRequest{
			RequestID: request.RequestID, ServerID: serverID, RepoID: request.RepoID,
			Path: request.Path, ObservedLockID: request.ObservedLockID, Role: request.Role,
			CounterpartyRealmAlias: request.CounterpartyRealmAlias, State: request.State,
			CreatedAt: request.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: request.UpdatedAt.UTC().Format(time.RFC3339Nano),
			ExpiresAt: request.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return projected
}

func pendingRepositoryCreations(serverID string, lifecycle *localrepo.Store) map[string]localrepo.Record {
	pending := make(map[string]localrepo.Record)
	if lifecycle == nil {
		return pending
	}
	for _, record := range lifecycle.List() {
		if record.ServerID == serverID && record.RepoID != "" && record.State == localrepo.StateRepositoryCreated {
			pending[record.RepoID] = record
		}
	}
	return pending
}

func repositoryReadiness(serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime) (bool, int) {
	pending := 0
	for _, repo := range view.Repositories {
		if repo.AttachmentPolicy != "required" || repo.State != "active" {
			continue
		}
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		if _, attached := attachments[key]; !attached {
			pending++
		}
	}
	return pending == 0, pending
}

// deletedRepositoryName is what to call a repository the server no longer
// carries.
//
// A12 in the seam register, from the owner's own screen: a row reading
// 5362797d-fed3-5aff-b6bd-640702a8bc4b, offering nothing, sitting exactly where
// he had to decide whether to download its archive. The name came from the
// server's view while the repository lived; once deleted the view stops
// carrying it, and the client had nothing of its own to fall back on.
//
// It did, though. The folder is right there in the same record - D:\AKTUALNE in
// his case - and its own name is what the interface shows for every other
// repository anyway. A record created by BeginCreate carries a display name; one
// created by BeginAttach, which is how a folder already on disk is adopted, does
// not, and that is the case that reached him.
//
// The UID stays as the last resort. A row that identifies nothing is still
// better than a row that names the wrong thing.
func deletedRepositoryName(record localrepo.Record) string {
	if name := strings.TrimSpace(record.DisplayName); name != "" {
		return name
	}
	if path := strings.TrimSpace(record.LocalPath); path != "" {
		if base := filepath.Base(filepath.Clean(path)); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return record.RepoID
}
