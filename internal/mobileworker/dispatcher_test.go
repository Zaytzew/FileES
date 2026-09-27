package mobileworker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/guiblob"
	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

type recordingJoiner struct {
	clientID string
	email    string
	err      error
}

func (j *recordingJoiner) RequestDesktopJoin(_ context.Context, clientID, email string) error {
	j.clientID = clientID
	j.email = email
	return j.err
}

func newDispatcher(t *testing.T, repo, access string) Dispatcher {
	t.Helper()
	auth := fakeAuthority{repoPath: repo, gen: 1, access: access}
	return Dispatcher{
		Browser:  Browser{Authority: auth, Reader: SVNReader{}},
		Appender: Appender{Authority: auth, Reader: SVNReader{}, Committer: SVNAppender{}, Ledger: Ledger{Dir: t.TempDir()}},
		ClientID: "client-1",
	}
}

func frameRequest(t *testing.T, rid string, op v1.Operation, payload any, body []byte) []byte {
	t.Helper()
	req, err := v1.NewRequest(rid, op, payload)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := v1.WriteFrame(&buf, v1.RequestMagic, header, body); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serve(t *testing.T, d Dispatcher, frame []byte) (v1.Response, []byte) {
	t.Helper()
	var out bytes.Buffer
	if err := d.Serve(context.Background(), bytes.NewReader(frame), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	header, payload, err := v1.ReadFrame(&out, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	resp, err := v1.ParseResponse(header)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	return resp, payload
}

func TestDispatchRefreshManifest(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "r")

	frame := frameRequest(t, uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "r"}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s, error = %+v", resp.Status, resp.Error)
	}
	var res v1.RefreshManifestResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Manifest == nil || res.Manifest.RepoRevision != 1 {
		t.Fatalf("unexpected manifest %+v", res)
	}
}

func TestDispatchReadObjectStreamsPayload(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "r")

	frame := frameRequest(t, uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "photos/2026/a.jpg"}, nil)
	resp, payload := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want hello", payload)
	}
}

func TestDispatchUploadThenStatus(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "rw")

	data := []byte("dispatched bytes")
	rid := uuid.NewString()
	up := frameRequest(t, rid, v1.OpUploadObject, v1.UploadObjectPayload{
		RepoID: "r", ParentPath: "mobile-uploads/photos", Filename: "disp.bin", Size: int64(len(data)), Sha256: sha(data),
	}, data)
	resp, _ := serve(t, d, up)
	if resp.Status != v1.StatusOK {
		t.Fatalf("upload status = %s error = %+v", resp.Status, resp.Error)
	}
	var res v1.UploadObjectResult
	json.Unmarshal(resp.Result, &res)
	if res.Outcome != v1.OutcomeCommitted || res.Revision != 2 {
		t.Fatalf("upload result %+v", res)
	}

	// GET_OPERATION_STATUS for that request must report COMMITTED.
	st := frameRequest(t, uuid.NewString(), v1.OpOperationStatus, v1.OperationStatusPayload{TargetRequestID: rid}, nil)
	statusResp, _ := serve(t, d, st)
	var statusRes v1.OperationStatusResult
	json.Unmarshal(statusResp.Result, &statusRes)
	if statusRes.State != v1.OpStateCommitted || statusRes.Revision != 2 {
		t.Fatalf("status result %+v", statusRes)
	}
}

func TestDispatchReadDeniedWithoutGrant(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "") // no grant

	frame := frameRequest(t, uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "top.txt"}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "access.denied" {
		t.Fatalf("expected access.denied error, got %+v", resp)
	}
}

// A cause writeError cannot name with a specific code must still reach an
// administrator, keyed by the same request_id the client's masked error
// carries - the client only ever sees "worker.failed"/"operation failed".
// "request_id reused with a different intent" (tree.go) is one such
// unmapped cause: reuse a committed request_id with different content.
func TestWriteErrorLogsGenericFailureCauseForAdministrator(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "rw")

	rid := uuid.NewString()
	first := packTree(t, map[string][]byte{"note.txt": []byte("v1")})
	frame := frameRequest(t, rid, v1.OpUploadTree, v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(first)), Sha256: sha(first),
	}, first)
	if resp, _ := serve(t, d, frame); resp.Status != v1.StatusOK {
		t.Fatalf("first UPLOAD_TREE: status = %s error = %+v", resp.Status, resp.Error)
	}

	second := packTree(t, map[string][]byte{"note.txt": []byte("v2, a different payload, same request_id")})
	frame = frameRequest(t, rid, v1.OpUploadTree, v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(second)), Sha256: sha(second),
	}, second)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "worker.failed" {
		t.Fatalf("expected worker.failed error on reused request_id, got %+v", resp)
	}

	logged, err := os.ReadFile(filepath.Join(d.Appender.Ledger.Dir, "errors.log"))
	if err != nil {
		t.Fatalf("errors.log: %v", err)
	}
	text := string(logged)
	for _, want := range []string{rid, "client-1", "UPLOAD_TREE", "request_id reused with a different intent"} {
		if !strings.Contains(text, want) {
			t.Fatalf("errors.log missing %q: %s", want, text)
		}
	}
}

// A cause already named by a specific code (access.denied here) must not
// also get a log line - that would just duplicate what the client's own
// error message already says, for every ordinary denial.
func TestWriteErrorDoesNotLogWhenTheCauseIsAlreadyNamed(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "") // no grant -> ErrAccessDenied

	body := []byte("zzzz")
	frame := frameRequest(t, uuid.NewString(), v1.OpUploadTree, v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body),
	}, body)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "access.denied" {
		t.Fatalf("expected access.denied error, got %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(d.Appender.Ledger.Dir, "errors.log")); !os.IsNotExist(err) {
		t.Fatalf("errors.log should not exist for an already-named cause, stat err = %v", err)
	}
}

func TestDispatchListRepositories(t *testing.T) {
	d := newDispatcher(t, "", "rw")

	frame := frameRequest(t, uuid.NewString(), v1.OpListRepositories, v1.ListRepositoriesPayload{}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s, error = %+v", resp.Status, resp.Error)
	}
	var res v1.ListRepositoriesResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.RealmAlias != "acme" || res.ServerDisplayName != "Serwer testowy" || len(res.Repositories) != 1 || res.Repositories[0].DisplayName != "JANCZEWICE" || res.Repositories[0].Purpose != "upload_shelf" {
		t.Fatalf("projection = %+v", res)
	}
}

func TestDispatchListDrawersUnsupportedWithoutStore(t *testing.T) {
	d := newDispatcher(t, "", "rw") // Drawers left nil, like newDispatcher's other fields.

	frame := frameRequest(t, uuid.NewString(), v1.OpListDrawers, v1.ListDrawersPayload{}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error.Code != "op.unsupported" {
		t.Fatalf("status = %s, error = %+v", resp.Status, resp.Error)
	}
}

func TestDispatchListDrawersProjection(t *testing.T) {
	d := newDispatcher(t, "", "rw")
	d.Browser.Drawers = fakeDrawerReader{state: guiblob.State{
		Version: uuid.NewString(),
		Data:    `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"d-1","name":"Archiwum"}],"repos":{"repo-1":"d-1"}}`,
	}}

	frame := frameRequest(t, uuid.NewString(), v1.OpListDrawers, v1.ListDrawersPayload{}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s, error = %+v", resp.Status, resp.Error)
	}
	var res v1.ListDrawersResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Drawers) != 1 || res.Drawers[0].ID != "d-1" || res.Assignments["repo-1"] != "d-1" {
		t.Fatalf("projection = %+v", res)
	}
}

func TestDispatchListDirectoryUsesPayload(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "r")

	frame := frameRequest(t, uuid.NewString(), v1.OpListDirectory, v1.ListDirectoryPayload{RepoID: "r", Path: ""}, nil)
	resp, payload := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s, error = %+v", resp.Status, resp.Error)
	}
	var meta v1.Manifest
	if err := json.Unmarshal(resp.Result, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Entries) != 0 {
		t.Fatalf("entries must travel in the payload, header had %d", len(meta.Entries))
	}
	var entries []v1.ManifestEntry
	if err := json.Unmarshal(payload, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("payload entries = %+v", entries)
	}
}

func TestDispatchRequestDesktopJoinUnsupportedWithoutJoiner(t *testing.T) {
	d := newDispatcher(t, "", "rw")
	frame := frameRequest(t, uuid.NewString(), v1.OpRequestDesktopJoin, v1.RequestDesktopJoinPayload{Email: "desk@example.test"}, nil)
	resp, payload := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "op.unsupported" {
		t.Fatalf("expected op.unsupported, got %+v", resp)
	}
	if len(payload) != 0 {
		t.Fatalf("join must not carry a body payload: %q", payload)
	}
	if strings.Contains(strings.ToLower(string(resp.Result)), "invite") || strings.Contains(strings.ToLower(string(resp.Result)), "token") {
		t.Fatalf("error result must not carry an invite: %s", resp.Result)
	}
}

func TestDispatchRequestDesktopJoinCallsJoiner(t *testing.T) {
	j := &recordingJoiner{}
	d := newDispatcher(t, "", "rw")
	d.Joiner = j
	frame := frameRequest(t, uuid.NewString(), v1.OpRequestDesktopJoin, v1.RequestDesktopJoinPayload{Email: "desk@example.test"}, nil)
	resp, payload := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s error = %+v", resp.Status, resp.Error)
	}
	if j.clientID != "client-1" || j.email != "desk@example.test" {
		t.Fatalf("joiner got clientID=%q email=%q", j.clientID, j.email)
	}
	if len(payload) != 0 {
		t.Fatalf("join must not carry a body payload: %q", payload)
	}
	if strings.Contains(strings.ToLower(string(resp.Result)), "invite") || strings.Contains(strings.ToLower(string(resp.Result)), "token") {
		t.Fatalf("join result must not carry an invite: %s", resp.Result)
	}
}

func TestDispatchRequestDesktopJoinJoinerDenied(t *testing.T) {
	j := &recordingJoiner{err: ErrAccessDenied}
	d := newDispatcher(t, "", "rw")
	d.Joiner = j
	frame := frameRequest(t, uuid.NewString(), v1.OpRequestDesktopJoin, v1.RequestDesktopJoinPayload{Email: "guest@example.test"}, nil)
	resp, payload := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "access.denied" {
		t.Fatalf("expected access.denied, got %+v", resp)
	}
	if len(payload) != 0 {
		t.Fatalf("denied join must not carry a body payload: %q", payload)
	}
}
