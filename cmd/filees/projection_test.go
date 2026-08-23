package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

func TestSyncProjectionKnowledgeKeepsDeletedRepositoryThroughRetention(t *testing.T) {
	serverID := "office"
	repoID := "00000000-0000-0000-0000-000000000099"
	lifecycle, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := lifecycle.EnsureConfiguredAttached(serverID, repoID, "svn+ssh://_filees-data@example/"+repoID, "rw", filepath.Join(t.TempDir(), "Archiwum"), "Archiwum")
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := lifecycle.BeginDetach(serverID, repoID, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := lifecycle.MarkServerDeleted(record.OperationID, deadline); err != nil {
		t.Fatal(err)
	}
	kit := filepath.Join(t.TempDir(), "archive.fkr")
	if _, err := lifecycle.MarkRecoveryPrepared(record.OperationID, kit); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(filepath.Join(t.TempDir(), "daemon.sock"))
	state := server.RegisterProjectedRepo(repoID, "stale", "", serverID, "", "active", false)
	view := clientview.View{RealmID: "00000000-0000-0000-0000-000000000001"}
	syncProjectionKnowledge(server, serverID, view, nil, lifecycle)

	summary := state.Summary()
	if !summary.ServerDeleted || !summary.LocalCleanupPending || !summary.RecoveryAvailable || summary.RetainUntil != deadline || summary.RecoveryOperationID != deleting.DetachOperationID || summary.LocalPath != record.LocalPath || summary.DisplayName != "Archiwum" {
		t.Fatalf("deleted projection=%+v", summary)
	}
}

type projectionStop func()

func (stop projectionStop) Stop(context.Context) error { stop(); return nil }

type projectionStarter struct {
	starts, stops int
	onStop        func()
}

func (starter *projectionStarter) Start(context.Context, reposupervisor.Desired) (reposupervisor.Instance, error) {
	starter.starts++
	return projectionStop(func() {
		starter.stops++
		if starter.onStop != nil {
			starter.onStop()
		}
	}), nil
}

func TestAttachedProjectionUsesServerAuthorityAndSkipsUnattachedRepositories(t *testing.T) {
	serverID := "office"
	attached := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	view := clientview.View{Repositories: []clientview.Repository{
		{RepoID: "00000000-0000-0000-0000-000000000002", DisplayName: "Not pinned", URL: "svn+ssh://_filees-client@example/two", Access: "r", State: "active"},
		{RepoID: attached.RepoID, DisplayName: "Documents", URL: "svn+ssh://_filees-client@example/one", Access: "rw", State: "disabled"},
	}}
	got := attachedProjection(serverID, view, map[reposupervisor.Key]repoRuntime{attached: {}})
	if len(got) != 1 {
		t.Fatalf("desired=%+v", got)
	}
	if got[0].Key != attached || got[0].Access != "rw" || got[0].State != "disabled" || got[0].URL != view.Repositories[1].URL || got[0].DisplayName != "Documents" {
		t.Fatalf("desired=%+v", got[0])
	}
}

func TestAttachedProjectionIncludesLocallyAttachedRepoMissingFromView(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000009"}
	runtimes := map[reposupervisor.Key]repoRuntime{
		key: {config: config.Repo{ID: key.RepoID, RepoURL: "svn+ssh://_filees-data@example/repo", Access: "rw", ServerID: serverID}},
	}
	// The server's projected view has not caught up with this brand-new
	// repository yet: it isn't in view.Repositories at all.
	view := clientview.View{Generation: 1}
	got := attachedProjection(serverID, view, runtimes)
	if len(got) != 1 || got[0].Key != key || got[0].State != "active" || got[0].Access != "rw" || got[0].URL != "svn+ssh://_filees-data@example/repo" {
		t.Fatalf("desired=%+v, want the locally attached repo started from local knowledge", got)
	}
}

func TestSyncProjectionKnowledgeDoesNotOrphanLocallyAttachedRepoMissingFromView(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000009"}
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	state := server.RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-data@example/repo", t.TempDir(), serverID, contract.AccessReadWrite)
	state.SetState(contract.StateActive)
	runtimes := map[reposupervisor.Key]repoRuntime{
		key: {config: config.Repo{ID: key.RepoID, RepoURL: "svn+ssh://_filees-data@example/repo", Access: "rw", ServerID: serverID}, state: state},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Exactly the moment right after CREATE_REPOSITORY/INITIAL_COMMIT succeed:
	// the repo is fully attached and running locally, but the server's
	// projected view (refreshed on its own poll interval) does not know
	// about it yet.
	view := clientview.View{Generation: 1}
	syncProjectionKnowledge(server, serverID, view, runtimes, nil)

	list, err := ipcclient.New(sock, "test").RepoList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repos) != 1 || list.Repos[0].ID != key.RepoID {
		t.Fatalf("locally attached repo missing from the projected view was orphaned from repo.list: %+v", list.Repos)
	}
}

func TestSyncProjectionKnowledgeKeepsPendingCreationLocalPath(t *testing.T) {
	serverID := "office"
	repoID := "00000000-0000-0000-0000-000000000010"
	localPath := filepath.Join(t.TempDir(), "Biblia Audio KIDS")
	lifecycle, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := lifecycle.BeginCreate(serverID, "Biblia Audio KIDS", localPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.MarkRepositoryCreated(record.OperationID, repoID, "svn+ssh://_filees-data@example/"+repoID); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(filepath.Join(t.TempDir(), "daemon.sock"))
	state := server.RegisterProjectedRepo(repoID, "Biblia Audio KIDS", "svn+ssh://_filees-data@example/"+repoID, serverID, contract.AccessReadWrite, contract.StateInitializing, false)
	view := clientview.View{Repositories: []clientview.Repository{{
		RepoID: repoID, DisplayName: "Biblia Audio KIDS", URL: "svn+ssh://_filees-data@example/" + repoID,
		Access: contract.AccessReadWrite, State: contract.StateInitializing, AttachmentPolicy: "optional",
	}}}
	syncProjectionKnowledge(server, serverID, view, nil, lifecycle)

	summary := state.Summary()
	if summary.Attached || summary.LocalPath != localPath || summary.State != contract.StateInitializing {
		t.Fatalf("pending creation summary=%+v", summary)
	}
}

func TestInitializingProjectionDoesNotStartAttachedPipeline(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	view := clientview.View{Generation: 1, Repositories: []clientview.Repository{{RepoID: key.RepoID, DisplayName: "Import", URL: "svn+ssh://_filees-data@example/repo", Access: "rw", State: "initializing"}}}
	desired := attachedProjection(serverID, view, map[reposupervisor.Key]repoRuntime{key: {}})
	starter := &projectionStarter{}
	supervisor, err := reposupervisor.New(starter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Apply(t.Context(), serverID, 1, desired); err != nil {
		t.Fatal(err)
	}
	if starter.starts != 0 {
		t.Fatalf("initializing repository started %d pipelines", starter.starts)
	}
}

func TestRepositoryReadinessRequiresEveryActiveRequiredAttachment(t *testing.T) {
	serverID := "office"
	required := "00000000-0000-0000-0000-000000000001"
	view := clientview.View{Repositories: []clientview.Repository{
		{RepoID: required, State: "active", AttachmentPolicy: "required"},
		{RepoID: "00000000-0000-0000-0000-000000000002", State: "active", AttachmentPolicy: "optional"},
		{RepoID: "00000000-0000-0000-0000-000000000003", State: "disabled", AttachmentPolicy: "required"},
	}}
	ready, pending := repositoryReadiness(serverID, view, nil)
	if ready || pending != 1 {
		t.Fatalf("readiness=%v pending=%d", ready, pending)
	}
	attachments := map[reposupervisor.Key]repoRuntime{{ServerID: serverID, RepoID: required}: {}}
	ready, pending = repositoryReadiness(serverID, view, attachments)
	if !ready || pending != 0 {
		t.Fatalf("attached readiness=%v pending=%d", ready, pending)
	}
}

func TestReconcileProjectedViewChangesLiveAuthority(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@old.example/repo", t.TempDir(), serverID, contract.AccessReadWrite)
	runtimes := map[reposupervisor.Key]repoRuntime{key: {state: state}}
	starter := &projectionStarter{}
	supervisor, err := reposupervisor.New(starter, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := clientview.View{Generation: 1, Repositories: []clientview.Repository{{RepoID: key.RepoID, DisplayName: "Repo", URL: "svn+ssh://_filees-client@new.example/repo", Access: "rw", State: "active"}}}
	server := ipcserver.New(t.TempDir() + "/projection.sock")
	if err := reconcileProjectedView(t.Context(), supervisor, server, serverID, view, runtimes, nil); err != nil {
		t.Fatal(err)
	}
	view.Generation = 2
	view.Repositories[0].Access = "r"
	starter.onStop = func() {
		if got := state.Summary().Access; got != contract.AccessReadWrite {
			t.Fatalf("rw authority changed to %q before writer stopped", got)
		}
	}
	if err := reconcileProjectedView(t.Context(), supervisor, server, serverID, view, runtimes, nil); err != nil {
		t.Fatal(err)
	}
	starter.onStop = nil
	if starter.starts != 2 || starter.stops != 1 {
		t.Fatalf("starts=%d stops=%d", starter.starts, starter.stops)
	}
	summary := state.Summary()
	if summary.Access != "r" || summary.URL != view.Repositories[0].URL {
		t.Fatalf("summary=%+v", summary)
	}
	if err := supervisor.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The editing policy reaches a running repository the same way access and URL
// do - through the projection, not through the stored attachment. Keeping it
// out of the local record means there is exactly one source of truth for a
// repository-wide, owner-controlled setting.
func TestAttachedProjectionCarriesEditingPolicyFromTheView(t *testing.T) {
	serverID := "office"
	locked := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	free := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000002"}
	view := clientview.View{Repositories: []clientview.Repository{
		{RepoID: locked.RepoID, DisplayName: "Rysunki", URL: "svn+ssh://_filees-client@example/one", Access: "rw", State: "active", EditingPolicy: clientview.EditingLockRequired},
		{RepoID: free.RepoID, DisplayName: "Notatki", URL: "svn+ssh://_filees-client@example/two", Access: "rw", State: "active"},
	}}

	got := attachedProjection(serverID, view, map[reposupervisor.Key]repoRuntime{locked: {}, free: {}})
	if len(got) != 2 {
		t.Fatalf("desired=%+v", got)
	}
	byRepo := map[string]reposupervisor.Desired{}
	for _, item := range got {
		byRepo[item.Key.RepoID] = item
	}
	if byRepo[locked.RepoID].EditingPolicy != clientview.EditingLockRequired {
		t.Fatalf("locked repository lost its policy: %+v", byRepo[locked.RepoID])
	}
	if byRepo[free.RepoID].EditingPolicy != clientview.EditingFree {
		t.Fatalf("free repository gained a policy: %+v", byRepo[free.RepoID])
	}
}

// A repository the server has not projected keeps the default. It cannot be
// anything else - there is no authority to read it from - and defaulting to
// the barrier would strand a locally attached repository with no way back.
func TestAttachedProjectionLeavesUnprojectedRepositoriesOnTheDefaultPolicy(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000003"}
	runtimes := map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, RepoURL: "svn+ssh://_filees-client@example/three", Access: "rw"}}}

	got := attachedProjection(serverID, clientview.View{}, runtimes)
	if len(got) != 1 {
		t.Fatalf("desired=%+v", got)
	}
	if got[0].EditingPolicy != clientview.EditingFree {
		t.Fatalf("unprojected repository invented a policy: %+v", got[0])
	}
}
