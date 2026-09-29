package sshtransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type failedReadSink struct{}

func (failedReadSink) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestStreamReadValidatesBeforeWritingAndBoundsBody(t *testing.T) {
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "a"})
	for _, tc := range []struct {
		name, body                     string
		size                           int64
		wrongID, wrongPath, failWriter bool
		wantErr                        bool
	}{
		{name: "ok", body: "hello", size: 5},
		{name: "empty", size: 0},
		{name: "short", body: "hell", size: 5, wantErr: true},
		{name: "excess", body: "hello!", size: 5, wantErr: true},
		{name: "wrong ID", body: "hello", size: 5, wrongID: true, wantErr: true},
		{name: "wrong path", body: "hello", size: 5, wrongPath: true, wantErr: true},
		{name: "disk full", body: "hello", size: 5, failWriter: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, path := req.RequestID, "a"
			if tc.wrongID {
				id = uuid.NewString()
			}
			if tc.wrongPath {
				path = "b"
			}
			resp, _ := v1.NewSuccess(id, v1.OpReadObject, v1.ReadObjectResult{Path: path, Size: tc.size, Sha256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"})
			header, _ := json.Marshal(resp)
			var frame, out bytes.Buffer
			if err := v1.WriteFrame(&frame, v1.ResponseMagic, header, []byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			var sink io.Writer = &out
			if tc.failWriter {
				sink = failedReadSink{}
			}
			_, _, err := readResponse(&frame, req, sink)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
			if (tc.wrongID || tc.wrongPath) && out.Len() != 0 {
				t.Fatal("wrote body before header validation")
			}
			if int64(out.Len()) > tc.size {
				t.Fatal("wrote excess body")
			}
		})
	}
}

// Real SSH: a failing sink must close the connection instead of waiting for a
// blocked sender to empty its window. The context is a test backstop only.
func TestStreamReadWriterFailureClosesSSH(t *testing.T) {
	host, _ := generateEd25519(t)
	signer, pub := generateEd25519(t)
	body := bytes.Repeat([]byte("x"), 8<<20)
	addr := startFakeServer(t, host, pub, func(raw, _ []byte) ([]byte, []byte, bool) {
		var req v1.Request
		json.Unmarshal(raw, &req)
		resp, _ := v1.NewSuccess(req.RequestID, req.Operation, v1.ReadObjectResult{Path: "a", Size: int64(len(body)), Sha256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"})
		header, _ := json.Marshal(resp)
		return header, body, true
	})
	tr, err := New(Config{Address: addr, User: "test", HostPublicKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "a"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = tr.DoStreamTo(ctx, req, failedReadSink{})
	if err == nil || ctx.Err() != nil {
		t.Fatalf("failure did not close session promptly: %v / %v", err, ctx.Err())
	}
}
