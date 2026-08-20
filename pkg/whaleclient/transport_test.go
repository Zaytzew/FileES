package whaleclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestPutWindowWaitsForContinueAndMatchesOffset(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	payloadBeforeContinue := make(chan bool, 1)
	gotPayload := make(chan string, 1)
	address := startWhaleSSH(t, hostSigner, clientSigner.PublicKey(), func(channel ssh.Channel) {
		reader := bufio.NewReader(channel)
		header, err := whale.ReadHeader(reader, whale.RequestMagic)
		if err != nil {
			return
		}
		request, err := whale.ParseRequest(header)
		if err != nil {
			return
		}
		firstByte := make(chan byte, 1)
		go func() {
			one := []byte{0}
			if _, err := io.ReadFull(reader, one); err == nil {
				firstByte <- one[0]
			}
		}()
		select {
		case <-firstByte:
			payloadBeforeContinue <- true
			return
		case <-time.After(75 * time.Millisecond):
			payloadBeforeContinue <- false
		}
		ready := whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "continue", Result: &whale.PutResult{GenerationID: request.Identity.GenerationID, Offset: request.Offset, State: whale.StateReceiving}}
		writeWhaleResponse(channel, ready)
		first := <-firstByte
		rest := make([]byte, request.PayloadSize-1)
		if _, err := io.ReadFull(reader, rest); err != nil {
			return
		}
		gotPayload <- string(append([]byte{first}, rest...))
		final := ready
		final.Status = "ok"
		final.Result.Offset += request.PayloadSize
		writeWhaleResponse(channel, final)
	})

	transport := configuredTransport(t, address, hostSigner.PublicKey(), clientPrivate)
	request := testRequest(whale.OpPutWindow)
	request.Offset = 7
	request.PayloadSize = 5
	response, err := transport.Do(context.Background(), request, bytesReader("abcde"))
	if err != nil {
		t.Fatal(err)
	}
	if <-payloadBeforeContinue {
		t.Fatal("client released payload before server confirmed the durable offset")
	}
	if got := <-gotPayload; got != "abcde" {
		t.Fatalf("payload = %q", got)
	}
	if response.Result.Offset != 12 {
		t.Fatalf("offset = %d", response.Result.Offset)
	}
}

func TestStatusRejectsMismatchedResponse(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startWhaleSSH(t, hostSigner, clientSigner.PublicKey(), func(channel ssh.Channel) {
		reader := bufio.NewReader(channel)
		header, _ := whale.ReadHeader(reader, whale.RequestMagic)
		request, _ := whale.ParseRequest(header)
		response := whale.Response{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: request.Operation, Status: "ok", Result: &whale.PutResult{GenerationID: request.Identity.GenerationID, State: whale.StateReceiving}}
		writeWhaleResponse(channel, response)
	})
	transport := configuredTransport(t, address, hostSigner.PublicKey(), clientPrivate)
	if _, err := transport.Do(context.Background(), testRequest(whale.OpPutStatus), nil); err == nil {
		t.Fatal("expected mismatched response to fail")
	}
}

func TestGetWindowStreamsExactRequestedRange(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startWhaleSSH(t, hostSigner, clientSigner.PublicKey(), func(channel ssh.Channel) {
		reader := bufio.NewReader(channel)
		header, _ := whale.ReadHeader(reader, whale.RequestMagic)
		request, _ := whale.ParseRequest(header)
		response := whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &whale.Result{GenerationID: request.Identity.GenerationID, TransferID: request.TransferID, Offset: request.Offset, State: whale.StateMaterializing, Revision: request.Revision, PayloadSize: request.PayloadSize}}
		writeWhaleResponse(channel, response)
		_, _ = channel.Write([]byte("GHIJ"))
	})
	transport := configuredTransport(t, address, hostSigner.PublicKey(), clientPrivate)
	request := testRequest(whale.OpGetWindow)
	request.Revision = 11
	request.TransferID = uuid.NewString()
	request.ConfirmationToken = uuid.NewString()
	request.Offset, request.PayloadSize = 6, 4
	var output bytes.Buffer
	response, err := transport.Do(context.Background(), request, nil, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "GHIJ" || response.Result.Offset != 6 {
		t.Fatalf("output=%q response=%+v", output.String(), response)
	}
}

func configuredTransport(t *testing.T, address string, hostKey ssh.PublicKey, private ed25519.PrivateKey) *Transport {
	t.Helper()
	root := t.TempDir()
	keyBlock, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(root, "id_ed25519")
	if err := os.WriteFile(identity, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(root, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{address}, hostKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(address)
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewTransport(Config{Address: host, Port: port, IdentityFile: identity, KnownHosts: known, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func startWhaleSSH(t *testing.T, hostSigner ssh.Signer, clientKey ssh.PublicKey, handle func(ssh.Channel)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !publicKeysEqual(key, clientKey) {
			return nil, errors.New("wrong client key")
		}
		return &ssh.Permissions{}, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for next := range channels {
			if next.ChannelType() != "session" {
				next.Reject(ssh.UnknownChannelType, "session required")
				continue
			}
			channel, requests, err := next.Accept()
			if err != nil {
				return
			}
			for request := range requests {
				if request.Type != "exec" {
					request.Reply(false, nil)
					continue
				}
				request.Reply(true, nil)
				handle(channel)
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				_ = channel.Close()
				return
			}
		}
	}()
	return listener.Addr().String()
}

func generateKey(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, private
}

func testRequest(operation whale.Operation) whale.Request {
	return whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: operation, Identity: whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/a.bin", GenerationID: uuid.NewString(), ExpectedSize: 20, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
}

func writeWhaleResponse(out io.Writer, response whale.Response) {
	raw, _ := json.Marshal(response)
	_ = whale.WriteFrame(out, whale.ResponseMagic, raw)
}

type stringReader struct{ value []byte }

func bytesReader(value string) io.Reader { return &stringReader{value: []byte(value)} }
func (r *stringReader) Read(out []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(out, r.value)
	r.value = r.value[n:]
	return n, nil
}

func publicKeysEqual(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && string(a.Marshal()) == string(b.Marshal())
}
