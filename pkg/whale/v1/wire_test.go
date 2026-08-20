package v1

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestPutWindowFrameLeavesPayloadStreaming(t *testing.T) {
	request := Request{Schema: Schema, RequestID: uuid.NewString(), Operation: OpPutWindow, Identity: validIdentity(), PayloadSize: 3}
	header, _ := json.Marshal(request)
	var wire bytes.Buffer
	if err := WriteFrame(&wire, RequestMagic, header); err != nil {
		t.Fatal(err)
	}
	wire.WriteString("abc")
	reader := bufio.NewReader(&wire)
	gotHeader, err := ReadHeader(reader, RequestMagic)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRequest(gotHeader)
	if err != nil || got != request {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	payload := make([]byte, 3)
	if _, err := reader.Read(payload); err != nil || string(payload) != "abc" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestPutWindowCannotClaimPastGenerationEnd(t *testing.T) {
	request := Request{Schema: Schema, RequestID: uuid.NewString(), Operation: OpPutWindow, Identity: validIdentity(), Offset: 1, PayloadSize: MaxWindowBytes}
	request.Identity.ExpectedSize = 2
	if err := request.Validate(); err == nil {
		t.Fatal("oversized window accepted")
	}
}

func TestGetDiscoveryAcceptsOnlyLogicalTargetAndSnapshot(t *testing.T) {
	identity := validIdentity()
	request := Request{Schema: Schema, RequestID: uuid.NewString(), Operation: OpGetDiscover, Identity: Identity{LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath}, Revision: 17}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Identity.GenerationID = identity.GenerationID
	if err := request.Validate(); err == nil {
		t.Fatal("GET discovery accepted caller-supplied generation")
	}
}
