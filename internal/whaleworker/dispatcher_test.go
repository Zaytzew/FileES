package whaleworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

func TestDispatcherPerformsOffsetHandshakeBeforeWindowAck(t *testing.T) {
	stateRoot := t.TempDir()
	service := PutService{
		Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")},
		Authority: putAuthority{repo: t.TempDir(), access: "rw"}, Reservations: &putReservations{}, Publisher: &putPublisher{rev: 1},
	}
	identity := putIdentity([]byte("abc"))
	request := putRequest(identity, 0, []byte("abc"))
	header, _ := json.Marshal(request)
	var input bytes.Buffer
	_ = whale.WriteFrame(&input, whale.RequestMagic, header)
	input.WriteString("abc")
	var output bytes.Buffer
	if err := (Dispatcher{Service: service, ClientID: "client"}).Serve(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&output)
	readyHeader, err := whale.ReadHeader(reader, whale.ResponseMagic)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := whale.ParseResponse(readyHeader)
	if err != nil || ready.Status != "continue" || ready.Result.Offset != 0 {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	ackHeader, err := whale.ReadHeader(reader, whale.ResponseMagic)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := whale.ParseResponse(ackHeader)
	if err != nil || ack.Status != "ok" || ack.Result.Offset != 3 || ack.Result.State != whale.StateCommitting {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestDispatcherUsesSharedErrorCatalog(t *testing.T) {
	identity := putIdentity([]byte("x"))
	request := putRequest(identity, 0, []byte("x"))
	header, _ := json.Marshal(request)
	var input bytes.Buffer
	_ = whale.WriteFrame(&input, whale.RequestMagic, header)
	input.WriteByte('x')
	stateRoot := t.TempDir()
	service := PutService{
		Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")},
		Authority: putAuthority{repo: t.TempDir(), access: "r"}, Reservations: &putReservations{}, Publisher: &putPublisher{rev: 1},
	}
	var output bytes.Buffer
	if err := (Dispatcher{Service: service, ClientID: "client"}).Serve(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&output)
	responseHeader, err := whale.ReadHeader(reader, whale.ResponseMagic)
	if err != nil {
		t.Fatal(err)
	}
	response, err := whale.ParseResponse(responseHeader)
	if err != nil || response.Status != "error" || response.Error.Code != "WHALE-2002" || response.Error.Key != "whale.access_denied" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestDispatcherProjectsWhaleCapacityDetails(t *testing.T) {
	request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutWindow}
	var output bytes.Buffer
	if err := (Dispatcher{}).writeError(&output, request, InsufficientSpaceError{AvailableBytes: 8_900_000_000, RequiredBytes: 18_000_000_000}); err != nil {
		t.Fatal(err)
	}
	header, err := whale.ReadHeader(bufio.NewReader(&output), whale.ResponseMagic)
	if err != nil {
		t.Fatal(err)
	}
	response, err := whale.ParseResponse(header)
	if err != nil || response.Error == nil || response.Error.Code != "WHALE-2005" || response.Error.Key != "whale.insufficient_space" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if response.Error.Details["available_bytes"] != "8900000000" || response.Error.Details["required_bytes"] != "18000000000" {
		t.Fatalf("details=%v", response.Error.Details)
	}
}
