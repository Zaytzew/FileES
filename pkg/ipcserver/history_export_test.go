package ipcserver

import (
	"path/filepath"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/historyexport"
)

type exportStub struct {
	begun      []historyexport.Request
	records    map[string]historyexport.Record
	beginErr   error
	confirmErr error
}

func (e *exportStub) Begin(req historyexport.Request) (historyexport.Record, error) {
	e.begun = append(e.begun, req)
	if e.beginErr != nil {
		return historyexport.Record{}, e.beginErr
	}
	if e.records == nil {
		e.records = map[string]historyexport.Record{}
	}
	rec := historyexport.Record{Request: req, State: historyexport.StatePlanning}
	e.records[req.ID] = rec
	return rec, nil
}

func (e *exportStub) Get(id string) (historyexport.Record, error) {
	rec, ok := e.records[id]
	if !ok {
		return historyexport.Record{}, historyexport.ErrUnknownOperation
	}
	return rec, nil
}

func (e *exportStub) Confirm(id string) (historyexport.Record, error) {
	rec, err := e.Get(id)
	if err != nil || e.confirmErr != nil {
		return rec, errorsJoin(err, e.confirmErr)
	}
	rec.State = historyexport.StateFetching
	e.records[id] = rec
	return rec, nil
}

func (e *exportStub) Cancel(id string) (historyexport.Record, error) {
	rec, err := e.Get(id)
	if err != nil {
		return rec, err
	}
	rec.State = historyexport.StateCancelled
	e.records[id] = rec
	return rec, nil
}

func errorsJoin(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func exportServer(t *testing.T) (*Server, *historyStub, *exportStub, contract.RepoHistorySnapshot) {
	t.Helper()
	stub := newHistoryStub(5)
	size := int64(17)
	stub.list = []contract.RepoHistoryEntry{{Name: "a.docx", Kind: "file", Size: &size, LastChangedRevision: 2}, {Name: "b", Kind: "dir", LastChangedRevision: 3}}
	s := historyServer(stub)
	exports := &exportStub{}
	s.SetHistoryExportService(exports)
	snap := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1", MomentUTC: historyAt(4)})
	return s, stub, exports, snap
}

func TestHistoryExportCapabilityNeedsBothServices(t *testing.T) {
	advertised := func(s *Server) bool {
		for _, capability := range s.capabilities() {
			if capability == contract.CapRepoHistoryExport {
				return true
			}
		}
		return false
	}
	stub := newHistoryStub(1)
	s := historyServer(stub)
	if advertised(s) {
		t.Fatal("export advertised without an export service")
	}
	s.SetHistoryExportService(&exportStub{})
	if !advertised(s) {
		t.Fatal("export not advertised")
	}
	stub.enabled = false
	if advertised(s) {
		t.Fatal("export advertised without a helper")
	}
}

func TestHistoryFetchPinsTheSnapshotAndNamesTheMomentInTheUsersZone(t *testing.T) {
	s, _, exports, snap := exportServer(t)
	parent := t.TempDir()
	op := historyCall[contract.RepoHistoryOperation](t, s, contract.CmdRepoHistoryFetch, contract.RepoHistoryFetchPayload{
		SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: parent, UTCOffsetMinutes: 120,
	})
	if op.State != historyexport.StatePlanning || op.Revision != 4 || op.DestinationParent != parent || len(op.OperationID) != 32 {
		t.Fatalf("operation = %+v", op)
	}
	if len(exports.begun) != 1 {
		t.Fatalf("begun = %d", len(exports.begun))
	}
	req := exports.begun[0]
	if req.RepoURL != "svn://office/projects" || req.RepositoryUUID != "uuid-1" || req.RepoName != "Projects" || req.Revision != 4 || req.Parent != parent {
		t.Fatalf("request = %+v", req)
	}
	wantMoment, _ := time.Parse(time.RFC3339Nano, snap.RequestedMoment)
	if !req.Moment.Equal(wantMoment) || req.Moment.Format("15:04 -07:00") != wantMoment.Add(2*time.Hour).UTC().Format("15:04")+" +02:00" {
		t.Fatalf("moment = %s, want %s shown at +02:00", req.Moment, wantMoment)
	}

	subtree := historyCall[contract.RepoHistoryOperation](t, s, contract.CmdRepoHistoryFetch, contract.RepoHistoryFetchPayload{
		SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionSubtree, Path: "01_EDITABLES", DestinationParent: parent,
	})
	if subtree.Subtree != "01_EDITABLES" || exports.begun[1].Subtree != "01_EDITABLES" {
		t.Fatalf("subtree operation = %+v", subtree)
	}
}

func TestHistoryFetchRefusesARepositoryThatChangedUnderTheSnapshot(t *testing.T) {
	s, stub, exports, snap := exportServer(t)
	stub.uuid = "uuid-2"
	payload := contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: t.TempDir()}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryFetch, payload); key != "history.snapshot_unknown" {
		t.Fatalf("key = %s", key)
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryFetch, payload); key != "history.snapshot_unknown" {
		t.Fatalf("a forgotten snapshot came back: %s", key)
	}
	if len(exports.begun) != 0 {
		t.Fatal("export began against another repository")
	}
}

func TestHistoryFetchOfSelectedFilesTakesSizesFromTheSnapshot(t *testing.T) {
	s, stub, exports, snap := exportServer(t)
	parent := t.TempDir()
	fetch := func(paths ...string) contract.RepoHistoryFetchPayload {
		return contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionPaths, Paths: paths, DestinationParent: parent}
	}
	op := historyCall[contract.RepoHistoryOperation](t, s, contract.CmdRepoHistoryFetch, fetch("01/a.docx"))
	if len(op.SelectedPaths) != 1 || op.SelectedPaths[0] != "01/a.docx" {
		t.Fatalf("operation = %+v", op)
	}
	if files := exports.begun[0].Files; len(files) != 1 || files[0] != (historyexport.Node{Path: "01/a.docx", Kind: "file", Size: 17}) {
		t.Fatalf("files = %+v", files)
	}
	if len(stub.listed) != 1 || stub.listed[0] != "01@4" {
		t.Fatalf("listed = %v", stub.listed)
	}
	for name, tc := range map[string]struct {
		payload contract.RepoHistoryFetchPayload
		key     string
	}{
		"folder picked as file":  {fetch("01/b"), "history.invalid_request"},
		"file not in snapshot":   {fetch("01/missing.txt"), "history.path_absent"},
		"folder not in snapshot": {fetch("missing/a.docx"), "history.path_absent"},
		"same file twice":        {fetch("01/a.docx", "01/a.docx"), "history.invalid_request"},
		"climbing path":          {fetch("../a.docx"), "history.invalid_request"},
		"nothing selected":       {fetch(), "history.invalid_request"},
		"unknown selection":      {contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: "everything", DestinationParent: parent}, "history.invalid_request"},
		"empty subtree":          {contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionSubtree, DestinationParent: parent}, "history.invalid_request"},
		"relative destination":   {contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: "relative"}, "proto.invalid_payload"},
		"zone out of range":      {contract.RepoHistoryFetchPayload{SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: parent, UTCOffsetMinutes: 15 * 60}, "history.invalid_request"},
	} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryFetch, tc.payload); key != tc.key {
			t.Errorf("%s: key = %s, want %s", name, key, tc.key)
		}
	}
	if len(exports.begun) != 1 {
		t.Fatalf("refused selections began exports: %d", len(exports.begun))
	}
}

func TestHistoryOperationIsOwnerOnlyAndMapsRefusals(t *testing.T) {
	s, _, exports, snap := exportServer(t)
	parent := t.TempDir()
	op := historyCall[contract.RepoHistoryOperation](t, s, contract.CmdRepoHistoryFetch, contract.RepoHistoryFetchPayload{
		SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: parent,
	})
	id := contract.RepoHistoryOperationPayload{OperationID: op.OperationID}
	if got := historyCall[contract.RepoHistoryOperation](t, s, contract.CmdRepoHistoryConfirm, id); got.State != historyexport.StateFetching {
		t.Fatalf("confirm = %+v", got)
	}

	exports.confirmErr = historyexport.ErrInsufficientSpace
	if key := historyRefusal(t, s, contract.CmdRepoHistoryConfirm, id); key != "history.insufficient_space" {
		t.Fatalf("no space: %s", key)
	}
	exports.confirmErr = historyexport.ErrState
	if key := historyRefusal(t, s, contract.CmdRepoHistoryConfirm, id); key != "history.operation_state" {
		t.Fatalf("wrong state: %s", key)
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryOperation, contract.RepoHistoryOperationPayload{OperationID: "0123456789abcdef0123456789abcdef"}); key != "history.operation_unknown" {
		t.Fatalf("unknown: %s", key)
	}
	exports.beginErr = historyexport.ErrDestination
	if key := historyRefusal(t, s, contract.CmdRepoHistoryFetch, contract.RepoHistoryFetchPayload{
		SnapshotID: snap.SnapshotID, Selection: contract.HistorySelectionAll, DestinationParent: filepath.Join(parent, "inside-wc"),
	}); key != "history.destination_refused" {
		t.Fatalf("destination: %s", key)
	}

	// The repository changed hands: not even the status is readable now.
	s.RegisterProjectedRepoPolicy("repo-1", "Projects", "svn://office/projects", "office", "rw", "active", "someone-else", "optional", true)
	for _, command := range []string{contract.CmdRepoHistoryOperation, contract.CmdRepoHistoryConfirm, contract.CmdRepoHistoryCancel} {
		if key := historyRefusal(t, s, command, id); key != "history.forbidden" {
			t.Fatalf("%s after ownership moved: %s", command, key)
		}
	}
	if exports.records[op.OperationID].State != historyexport.StateFetching {
		t.Fatal("a refused call changed the operation")
	}
}
