package whaleworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	whale "filees/pkg/whale/v1"
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
