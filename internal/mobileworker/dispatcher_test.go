package mobileworker

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

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
		RepoID: "r", ParentPath: "photos", Filename: "disp.bin", Size: int64(len(data)), Sha256: sha(data),
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

func TestDispatchUnsupportedOperation(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "r")

	frame := frameRequest(t, uuid.NewString(), v1.OpListDirectory, v1.ListDirectoryPayload{RepoID: "r", Path: "photos"}, nil)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "op.unsupported" {
		t.Fatalf("expected op.unsupported error, got %+v", resp)
	}
}
