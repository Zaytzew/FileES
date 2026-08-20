package whaleclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

// TestTransportOpenBSDE2E is opt-in because it needs an isolated forced-command
// sshd fixture. It is the Windows-to-OpenBSD proof for the same Transport used
// by the daemon; ordinary unit tests use an in-process SSH server.
func TestTransportOpenBSDE2E(t *testing.T) {
	address := os.Getenv("FILEES_WHALE_E2E_ADDRESS")
	identityFile := os.Getenv("FILEES_WHALE_E2E_IDENTITY")
	knownHosts := os.Getenv("FILEES_WHALE_E2E_KNOWN_HOSTS")
	repoID := os.Getenv("FILEES_WHALE_E2E_REPO_ID")
	if address == "" || identityFile == "" || knownHosts == "" || repoID == "" {
		t.Skip("isolated OpenBSD Whale SSH fixture is not configured")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewTransport(Config{Address: host, Port: port, IdentityFile: identityFile, KnownHosts: knownHosts, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("Windows to OpenBSD Whale forced-command E2E\n")
	digest := sha256.Sum256(content)
	object := whale.Identity{LogicalRepoID: repoID, LogicalPath: "media/windows-openbsd.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	window := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutWindow, Identity: object, PayloadSize: int64(len(content))}
	accepted, err := transport.Do(ctx, window, bytes.NewReader(content))
	if err != nil || accepted.Result.Offset != int64(len(content)) {
		t.Fatalf("PUT window=%+v err=%v", accepted, err)
	}
	commit := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutCommit, Identity: object}
	published, err := transport.Do(ctx, commit, nil)
	if err != nil || published.Result.State != whale.StatePublished || published.Result.Revision < 1 {
		t.Fatalf("PUT commit=%+v err=%v", published, err)
	}
	quote := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetQuote, Identity: object, Revision: published.Result.Revision}
	quoted, err := transport.Do(ctx, quote, nil)
	if err != nil || quoted.Result.State != whale.StateAwaitingConfirmation {
		t.Fatalf("GET quote=%+v err=%v", quoted, err)
	}
	transferID, confirmation := uuid.NewString(), uuid.NewString()
	get := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetWindow, Identity: object, Revision: published.Result.Revision, TransferID: transferID, ConfirmationToken: confirmation, PayloadSize: int64(len(content))}
	var received bytes.Buffer
	if _, err := transport.Do(ctx, get, nil, &received); err != nil || !bytes.Equal(received.Bytes(), content) {
		t.Fatalf("GET payload=%q err=%v", received.Bytes(), err)
	}
	release := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetRelease, Identity: object, Revision: published.Result.Revision, TransferID: transferID, ConfirmationToken: confirmation}
	if _, err := transport.Do(ctx, release, nil); err != nil {
		t.Fatalf("GET release: %v", err)
	}
}
