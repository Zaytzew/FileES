package mobileclient

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

func packTreeForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.SetComment(v1.TreePackComment); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dropUploadTreeAckTransport wraps a real transport and, exactly once,
// simulates a lost acknowledgment: the wrapped UPLOAD_TREE operation
// actually runs against the real dispatcher (so a real commit lands), but
// the caller gets a transport error instead of the real response - the
// exact ambiguity UploadTree's GET_OPERATION_STATUS follow-up exists to
// resolve. Every other call (including that follow-up) passes through.
type dropUploadTreeAckTransport struct {
	inner   Transport
	tripped bool
}

func (t *dropUploadTreeAckTransport) Do(ctx context.Context, req v1.Request, reqPayload []byte) (v1.Response, []byte, error) {
	resp, payload, err := t.inner.Do(ctx, req, reqPayload)
	if req.Operation == v1.OpUploadTree && !t.tripped {
		t.tripped = true
		if err == nil && resp.Status == v1.StatusOK {
			return v1.Response{}, nil, errors.New("simulated: connection dropped before ack")
		}
	}
	return resp, payload, err
}

type alwaysFailTransport struct{}

func (alwaysFailTransport) Do(context.Context, v1.Request, []byte) (v1.Response, []byte, error) {
	return v1.Response{}, nil, errors.New("simulated: no connection at all")
}

func TestUploadTreeRecoversFromLostAckViaOperationStatus(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")
	c.Transport = &dropUploadTreeAckTransport{inner: c.Transport}

	body := packTreeForTest(t, map[string][]byte{"note.txt": []byte("hello from a flaky link")})
	if err := c.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, body); err != nil {
		t.Fatalf("UploadTree with a lost ack should recover via GET_OPERATION_STATUS, got: %v", err)
	}

	data, err := c.Read(context.Background(), "repo-1", "mobile-uploads/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from a flaky link" {
		t.Fatalf("committed content = %q", data)
	}
}

func TestUploadTreeStillFailsWhenTheServerNeverSawIt(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")
	c.Transport = alwaysFailTransport{}

	body := packTreeForTest(t, map[string][]byte{"note.txt": []byte("never arrives")})
	if err := c.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, body); err == nil {
		t.Fatal("expected an error when the status check also cannot reach the server")
	}
}
