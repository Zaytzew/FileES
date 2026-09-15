package ipcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

var historyEpoch = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

// historyStub is a repository with one commit per minute after historyEpoch.
type historyStub struct {
	enabled bool
	commits []HistoryLogEntry // newest first
	list    []contract.RepoHistoryEntry
	listed  []string
	logs    int
}

func newHistoryStub(n int) *historyStub {
	stub := &historyStub{enabled: true}
	for rev := n; rev >= 1; rev-- {
		stub.commits = append(stub.commits, HistoryLogEntry{Revision: int64(rev), Date: historyAt(rev)})
	}
	return stub
}

func historyAt(minute int) string {
	return historyEpoch.Add(time.Duration(minute) * time.Minute).Format("2006-01-02T15:04:05.000000Z")
}

func (h *historyStub) HistoryEnabled() bool { return h.enabled }

func (h *historyStub) HistoryRepositoryUUID(context.Context, string, string) (string, error) {
	return "uuid-1", nil
}

func (h *historyStub) HistoryRevisionAt(_ context.Context, _, _ string, moment time.Time) (int64, string, error) {
	for _, commit := range h.commits {
		if date, _ := time.Parse(time.RFC3339Nano, commit.Date); !date.After(moment) {
			return commit.Revision, commit.Date, nil
		}
	}
	return 0, "", nil
}

func (h *historyStub) HistoryLog(_ context.Context, _, _ string, newest, oldest int64, limit int) ([]HistoryLogEntry, error) {
	h.logs++
	var out []HistoryLogEntry
	for _, commit := range h.commits {
		if commit.Revision <= newest && commit.Revision >= oldest && len(out) < limit {
			out = append(out, commit)
		}
	}
	return out, nil
}

func (h *historyStub) HistoryList(_ context.Context, _, _, path string, revision int64) ([]contract.RepoHistoryEntry, error) {
	h.listed = append(h.listed, fmt.Sprintf("%s@%d", path, revision))
	if path == "missing" {
		return nil, ErrHistoryPathAbsent
	}
	return h.list, nil
}

func historyServer(stub *historyStub) *Server {
	s := New("unused")
	s.SetHistoryService(stub)
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner"})
	s.RegisterProjectedRepoPolicy("repo-1", "Projects", "svn://office/projects", "office", "rw", "active", "owner", "optional", true)
	return s
}

func historyCall[T any](t *testing.T, s *Server, command string, payload any) T {
	t.Helper()
	resp := s.dispatch(lifecycleRequest(command, payload))
	if resp.Status != contract.StatusOK {
		t.Fatalf("%s refused: %+v", command, resp.Error)
	}
	var out T
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func historyRefusal(t *testing.T, s *Server, command string, payload any) string {
	t.Helper()
	resp := s.dispatch(lifecycleRequest(command, payload))
	if resp.Status == contract.StatusOK || resp.Error == nil {
		t.Fatalf("%s accepted: %s", command, resp.Result)
	}
	return resp.Error.MessageKey
}

func TestHistoryCapabilityFollowsTheHelper(t *testing.T) {
	advertised := func(s *Server) bool {
		for _, capability := range s.capabilities() {
			if capability == contract.CapRepoHistory {
				return true
			}
		}
		return false
	}
	if advertised(New("unused")) {
		t.Fatal("advertised without a service")
	}
	stub := newHistoryStub(1)
	stub.enabled = false
	s := historyServer(stub)
	if advertised(s) {
		t.Fatal("advertised without a helper")
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"}); key != "history.unavailable" {
		t.Fatalf("without a helper: %s", key)
	}
	stub.enabled = true
	if !advertised(s) {
		t.Fatal("not advertised with a helper")
	}
}

// Until the server filters history by grant epoch, a guest with valid
// credentials gets a refusal, and the repository is not even asked.
func TestHistoryIsOwnerOnly(t *testing.T) {
	stub := newHistoryStub(3)
	s := historyServer(stub)
	snap := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"})
	if snap.Revision != 3 || snap.RepositoryUUID != "uuid-1" || snap.SnapshotID == "" || snap.SavedAt != historyAt(3) {
		t.Fatalf("snapshot = %+v", snap)
	}

	s.RegisterProjectedRepoPolicy("guest-repo", "Shared", "svn://office/shared", "office", "rw", "active", "someone-else", "optional", true)
	shelf := s.RegisterProjectedRepoPolicy("shelf", "Shelf", "svn://office/shelf", "office", "r", "active", "owner", "optional", false)
	shelf.SetPurpose(contract.RepoPurposeUploadShelf)
	for name, payload := range map[string]contract.RepoHistoryResolvePayload{
		"guest repository":       {ServerID: "office", RepoID: "guest-repo"},
		"upload shelf":           {ServerID: "office", RepoID: "shelf"},
		"unknown repository":     {ServerID: "office", RepoID: "nope"},
		"server not activated":   {ServerID: "home", RepoID: "repo-1"},
		"repository elsewhere":   {ServerID: "home", RepoID: "guest-repo"},
		"no realm on activation": {},
	} {
		t.Run(name, func(t *testing.T) {
			target := s
			if name == "no realm on activation" {
				target = historyServer(stub)
				target.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal})
				payload = contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"}
			}
			if key := historyRefusal(t, target, contract.CmdRepoHistoryResolve, payload); key != "history.forbidden" {
				t.Fatalf("key = %s", key)
			}
		})
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office"}); key != "proto.invalid_payload" {
		t.Fatalf("missing repo id: %s", key)
	}

	// Losing ownership closes a snapshot already handed out.
	s.RegisterProjectedRepoPolicy("repo-1", "Projects", "svn://office/projects", "office", "rw", "active", "someone-else", "optional", true)
	if key := historyRefusal(t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: snap.SnapshotID}); key != "history.forbidden" {
		t.Fatalf("snapshot after ownership moved: %s", key)
	}
	if stub.logs != 0 || len(stub.listed) != 0 {
		t.Fatalf("repository read for refused requests: logs=%d listed=%v", stub.logs, stub.listed)
	}
}

func TestHistoryResolveBoundaryAndCommitPages(t *testing.T) {
	stub := newHistoryStub(120)
	s := historyServer(stub)
	moment := func(minute int) string {
		return historyEpoch.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339)
	}
	resolve := func(payload contract.RepoHistoryResolvePayload) contract.RepoHistorySnapshot {
		payload.ServerID, payload.RepoID = "office", "repo-1"
		return historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, payload)
	}
	snap := resolve(contract.RepoHistoryResolvePayload{MomentUTC: moment(40)})
	if snap.Revision != 40 || snap.SavedAt != historyAt(40) {
		t.Fatalf("at a commit's own moment: %+v", snap)
	}
	if before := resolve(contract.RepoHistoryResolvePayload{MomentUTC: moment(40), Boundary: contract.HistoryBoundaryBefore}); before.Revision != 39 {
		t.Fatalf("before a commit's moment: %+v", before)
	}
	if early := resolve(contract.RepoHistoryResolvePayload{MomentUTC: moment(0)}); early.Revision != 0 || early.SavedAt != "" {
		t.Fatalf("before every commit: %+v", early)
	}
	for name, payload := range map[string]contract.RepoHistoryResolvePayload{
		"unknown boundary": {ServerID: "office", RepoID: "repo-1", Boundary: "after"},
		"unparsed moment":  {ServerID: "office", RepoID: "repo-1", MomentUTC: "yesterday"},
	} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryResolve, payload); key != "history.invalid_request" {
			t.Fatalf("%s: %s", name, key)
		}
	}

	var got []int64
	pages := 0
	page := historyCall[contract.RepoHistoryCommitsResult](t, s, contract.CmdRepoHistoryCommits, contract.RepoHistoryCommitsPayload{SnapshotID: snap.SnapshotID, From: moment(10), To: moment(110)})
	for {
		pages++
		for _, commit := range page.Commits {
			got = append(got, commit.Revision)
		}
		if page.NextCursor == "" {
			break
		}
		page = historyCall[contract.RepoHistoryCommitsResult](t, s, contract.CmdRepoHistoryCommits, contract.RepoHistoryCommitsPayload{SnapshotID: snap.SnapshotID, Cursor: page.NextCursor})
	}
	if pages != 3 || len(got) != 101 || got[0] != 110 || got[100] != 10 {
		t.Fatalf("pages=%d commits=%d first=%v last=%v", pages, len(got), got[0], got[len(got)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]-1 {
			t.Fatalf("gap or repeat between pages at %d: %v", i, got[i-1:i+1])
		}
	}
	// The interval is independent of the snapshot's moment (r40).
	if page.Commits[0].Revision != 10 {
		t.Fatalf("last page = %+v", page.Commits)
	}

	empty := historyCall[contract.RepoHistoryCommitsResult](t, s, contract.CmdRepoHistoryCommits, contract.RepoHistoryCommitsPayload{SnapshotID: snap.SnapshotID, From: moment(-10), To: moment(0)})
	if len(empty.Commits) != 0 || empty.NextCursor != "" || empty.Commits == nil {
		t.Fatalf("interval before every commit = %+v", empty)
	}
	for name, payload := range map[string]contract.RepoHistoryCommitsPayload{
		"reversed interval": {SnapshotID: snap.SnapshotID, From: moment(20), To: moment(10)},
		"missing interval":  {SnapshotID: snap.SnapshotID},
		"reversed cursor":   {SnapshotID: snap.SnapshotID, Cursor: "r5:9"},
		"garbage cursor":    {SnapshotID: snap.SnapshotID, Cursor: "o50"},
		"cursor from r0":    {SnapshotID: snap.SnapshotID, Cursor: "r5:0"},
	} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryCommits, payload); key != "history.invalid_request" {
			t.Fatalf("%s: %s", name, key)
		}
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryCommits, contract.RepoHistoryCommitsPayload{SnapshotID: "nope", From: moment(10), To: moment(20)}); key != "history.snapshot_unknown" {
		t.Fatalf("unknown snapshot: %s", key)
	}
}

func TestHistoryChangesArePagedByPath(t *testing.T) {
	stub := newHistoryStub(2)
	for i := 0; i < historyChangesPage+1; i++ {
		stub.commits[0].Changes = append(stub.commits[0].Changes, contract.RepoHistoryChange{Path: fmt.Sprintf("dir/f%04d", historyChangesPage-i), Action: "M"})
	}
	s := historyServer(stub)
	snap := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"})

	commits := historyCall[contract.RepoHistoryCommitsResult](t, s, contract.CmdRepoHistoryCommits, contract.RepoHistoryCommitsPayload{SnapshotID: snap.SnapshotID, From: historyAt(0), To: historyAt(10)})
	if len(commits.Commits) != 2 || commits.Commits[0].ChangedCount != historyChangesPage+1 {
		t.Fatalf("commits = %+v", commits)
	}
	first := historyCall[contract.RepoHistoryChangesResult](t, s, contract.CmdRepoHistoryChanges, contract.RepoHistoryChangesPayload{SnapshotID: snap.SnapshotID, Revision: 2})
	if len(first.Changed) != historyChangesPage || first.Changed[0].Path != "dir/f0000" || first.NextCursor == "" {
		t.Fatalf("first page: %d changes, first %q, cursor %q", len(first.Changed), first.Changed[0].Path, first.NextCursor)
	}
	second := historyCall[contract.RepoHistoryChangesResult](t, s, contract.CmdRepoHistoryChanges, contract.RepoHistoryChangesPayload{SnapshotID: snap.SnapshotID, Revision: 2, Cursor: first.NextCursor})
	if len(second.Changed) != 1 || second.Changed[0].Path != fmt.Sprintf("dir/f%04d", historyChangesPage) || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	for name, payload := range map[string]contract.RepoHistoryChangesPayload{
		"revision zero":        {SnapshotID: snap.SnapshotID},
		"revision not in log":  {SnapshotID: snap.SnapshotID, Revision: 9},
		"cursor past the end":  {SnapshotID: snap.SnapshotID, Revision: 2, Cursor: "o999"},
		"cursor of other kind": {SnapshotID: snap.SnapshotID, Revision: 2, Cursor: "r2:1"},
	} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryChanges, payload); key != "history.invalid_request" {
			t.Fatalf("%s: %s", name, key)
		}
	}
}

func TestHistoryListReadsTheSnapshotRevision(t *testing.T) {
	stub := newHistoryStub(5)
	size := int64(17)
	stub.list = []contract.RepoHistoryEntry{{Name: "a.docx", Kind: "file", Size: &size, LastChangedRevision: 2}, {Name: "b", Kind: "dir", LastChangedRevision: 3}}
	s := historyServer(stub)
	snap := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1", MomentUTC: historyAt(3)})

	listing := historyCall[contract.RepoHistoryListResult](t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: snap.SnapshotID, Path: "01_EDITABLES/2026 wiosna"})
	if listing.Revision != 3 || listing.Path != "01_EDITABLES/2026 wiosna" || len(listing.Entries) != 2 || *listing.Entries[0].Size != 17 {
		t.Fatalf("listing = %+v", listing)
	}
	if len(stub.listed) != 1 || stub.listed[0] != "01_EDITABLES/2026 wiosna@3" {
		t.Fatalf("listed = %v", stub.listed)
	}
	for _, path := range []string{"../x", "/abs", "a//b", `a\b`, "a/./b", "trailing/", "a\x01b"} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: snap.SnapshotID, Path: path}); key != "history.invalid_request" {
			t.Fatalf("path %q: %s", path, key)
		}
	}
	if len(stub.listed) != 1 {
		t.Fatalf("an invalid path reached the repository: %v", stub.listed)
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: snap.SnapshotID, Path: "missing"}); key != "history.path_absent" {
		t.Fatalf("absent folder: %s", key)
	}

	// A repository now answering at another URL may be another repository.
	s.RegisterProjectedRepoPolicy("repo-1", "Projects", "svn://office/projects-recreated", "office", "rw", "active", "owner", "optional", true)
	if key := historyRefusal(t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: snap.SnapshotID}); key != "history.snapshot_unknown" {
		t.Fatalf("snapshot after the URL changed: %s", key)
	}
	fresh := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"})
	historyCall[contract.RepoHistoryListResult](t, s, contract.CmdRepoHistoryList, contract.RepoHistoryListPayload{SnapshotID: fresh.SnapshotID})
}

func TestHistorySnapshotsAreBounded(t *testing.T) {
	var store historySnapshotStore
	first, err := store.remember(historySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < historySnapshotBudget; i++ {
		if _, err := store.remember(historySnapshot{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := store.lookup(first); ok {
		t.Fatal("oldest snapshot kept past the budget")
	}
	if len(store.byID) != historySnapshotBudget || len(store.order) != historySnapshotBudget {
		t.Fatalf("store holds %d/%d", len(store.byID), len(store.order))
	}
}
