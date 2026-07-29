package recoverykit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestDownloadUsesPinnedRecoveryCapabilityAndVerifiesArchive(t *testing.T) {
	dump := []byte("SVN-fs-dump-format-version: 3\n")
	digest := sha256.Sum256(dump)
	now := time.Now().UTC()
	operationID, realmID := uuid.NewString(), uuid.NewString()
	manifest := repoworker.RecoveryManifest{
		Schema: repoworker.RecoveryManifestSchema, OperationID: operationID, RealmID: realmID,
		CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour),
		Archives: []repoworker.RecoveryArchive{{
			ArchiveID: uuid.NewString(), RepoID: uuid.NewString(),
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(dump)),
		}},
	}
	hostSigner := testRecoverySigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP unavailable: %v", err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	knownHost := knownHostLine(address, hostSigner.PublicKey())
	kit, _, err := Create(address, knownHost, manifest)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, _, _, _, err := ssh.ParseAuthorizedKey([]byte(kit.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- serveRecoveryTestSSH(raw, hostSigner, clientPublic, manifest, dump)
	}()
	output := t.TempDir()
	paths, err := Download(context.Background(), kit, output, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(output, manifest.Archives[0].RepoID+".svndump") {
		t.Fatalf("paths=%v", paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil || string(raw) != string(dump) {
		t.Fatalf("download=%q err=%v", raw, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func testRecoverySigner(t *testing.T) ssh.Signer {
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

func knownHostLine(address string, key ssh.PublicKey) string {
	return "[" + strings.ReplaceAll(address, ":", "]:") + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func serveRecoveryTestSSH(raw net.Conn, host ssh.Signer, clientKey ssh.PublicKey, manifest repoworker.RecoveryManifest, dump []byte) error {
	defer raw.Close()
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != recoveryUser || string(key.Marshal()) != string(clientKey.Marshal()) {
			return nil, errors.New("recovery key rejected")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	connection, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return err
	}
	defer connection.Close()
	go ssh.DiscardRequests(requests)
	served := 0
	for channelRequest := range channels {
		if channelRequest.ChannelType() != "session" {
			_ = channelRequest.Reject(ssh.Prohibited, "session only")
			continue
		}
		channel, channelRequests, err := channelRequest.Accept()
		if err != nil {
			return err
		}
		request := <-channelRequests
		var execPayload struct{ Command string }
		if request.Type != "exec" || ssh.Unmarshal(request.Payload, &execPayload) != nil || execPayload.Command != recoveryCommand {
			_ = request.Reply(false, nil)
			_ = channel.Close()
			continue
		}
		_ = request.Reply(true, nil)
		rawRequest, _ := io.ReadAll(io.LimitReader(channel, 1024))
		line := strings.TrimSpace(string(rawRequest))
		switch {
		case line == "list "+manifest.OperationID:
			_ = json.NewEncoder(channel).Encode(manifest)
		case line == "get "+manifest.OperationID+" "+manifest.Archives[0].ArchiveID:
			_, _ = channel.Write(dump)
		default:
			return errors.New("unexpected recovery request")
		}
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, 0)
		_, _ = channel.SendRequest("exit-status", false, status)
		_ = channel.Close()
		served++
		if served == 2 {
			return nil
		}
	}
	return nil
}
