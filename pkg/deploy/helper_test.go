package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestHelperAllowsOnlyPinnedWorkerAndGenerateIdentityProtocol(t *testing.T) {
	worker := newTestSigner(t)
	op, clientID := uuid.NewString(), "client-a"
	helper, err := StartHelper(context.Background(), HelperConfig{
		OperationID: op, ClientID: clientID, WorkerKey: worker.PublicKey(),
		Identity: IdentityGenerator{Root: filepath.Join(t.TempDir(), "identity")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer helper.Close()
	client := dialHelper(t, helper, worker)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start(HelperCommand); err != nil {
		t.Fatal(err)
	}
	request := HelperRequest{Schema: HelperSchema, OperationID: op, RequestID: uuid.NewString(), ClientID: clientID, Action: ActionGenerateIdentity}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	var response HelperResponse
	if err := json.NewDecoder(stdout).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if response.Status != "ok" || response.Identity == nil || response.Identity.Fingerprint == "" || response.OperationID != op {
		t.Fatalf("response=%#v", response)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private_key_path") || strings.Contains(string(raw), filepath.Join("identity", "id_ed25519")) {
		t.Fatalf("helper leaked client-private identity state: %s", raw)
	}
}

func TestHelperRejectsWrongWorkerKeyShellAndOtherCommand(t *testing.T) {
	worker := newTestSigner(t)
	helper, err := StartHelper(context.Background(), HelperConfig{
		OperationID: uuid.NewString(), ClientID: "client-a", WorkerKey: worker.PublicKey(),
		Identity: IdentityGenerator{Root: filepath.Join(t.TempDir(), "identity")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer helper.Close()
	wrongConfig := helperClientConfig(helper, newTestSigner(t))
	if conn, err := ssh.Dial("tcp", helper.Endpoint().Address, wrongConfig); err == nil {
		conn.Close()
		t.Fatal("helper accepted an unpinned worker key")
	}
	client := dialHelper(t, helper, worker)
	defer client.Close()
	shell, _ := client.NewSession()
	if err := shell.Shell(); err == nil {
		t.Fatal("helper accepted shell")
	}
	_ = shell.Close()
	other, _ := client.NewSession()
	if err := other.Run("uname -a"); err == nil {
		t.Fatal("helper accepted arbitrary command")
	}
}

func TestHelperIsLoopbackOnlyAndStopsWithContext(t *testing.T) {
	worker := newTestSigner(t)
	if _, err := StartHelper(context.Background(), HelperConfig{ListenAddr: "0.0.0.0:0", WorkerKey: worker.PublicKey()}); err == nil {
		t.Fatal("helper accepted non-loopback listen address")
	}
	ctx, cancel := context.WithCancel(context.Background())
	helper, err := StartHelper(ctx, HelperConfig{
		OperationID: uuid.NewString(), ClientID: "client-a", WorkerKey: worker.PublicKey(),
		Identity: IdentityGenerator{Root: filepath.Join(t.TempDir(), "identity")},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := dialHelper(t, helper, worker)
	defer client.Close()
	cancel()
	select {
	case <-helper.Done():
	case <-time.After(time.Second):
		t.Fatal("helper did not stop with deploy context")
	}
	if session, err := client.NewSession(); err == nil {
		session.Close()
		t.Fatal("active helper connection survived deploy context")
	}
}

func TestHelperRequestRejectsTrailingJSON(t *testing.T) {
	op, clientID := uuid.NewString(), "client-a"
	request := `{"schema":"` + HelperSchema + `","operation_id":"` + op + `","request_id":"` + uuid.NewString() + `","client_id":"` + clientID + `","action":"` + ActionGenerateIdentity + `"}`
	decoder := json.NewDecoder(strings.NewReader(request + ` {"action":"second"}`))
	if _, err := decodeHelperRequest(decoder, op, clientID); err == nil {
		t.Fatal("helper accepted trailing JSON")
	}
}

func TestExecPayloadMustContainExactlyOneCommand(t *testing.T) {
	payload := ssh.Marshal(struct{ Command string }{HelperCommand})
	if command, ok := parseExecPayload(payload); !ok || command != HelperCommand {
		t.Fatalf("valid payload rejected: %q, %v", command, ok)
	}
	if _, ok := parseExecPayload(append(payload, 0)); ok {
		t.Fatal("exec payload with trailing bytes accepted")
	}
}

func dialHelper(t *testing.T, helper *Helper, signer ssh.Signer) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", helper.Endpoint().Address, helperClientConfig(helper, signer))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func helperClientConfig(helper *Helper, signer ssh.Signer) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: HelperUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if keysEqual(key, helper.config.HostSigner.PublicKey()) {
				return nil
			}
			return errors.New("unexpected helper host key")
		},
	}
}

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
