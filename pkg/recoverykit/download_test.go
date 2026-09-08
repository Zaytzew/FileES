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
	"sync"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestDownloadUsesPinnedRecoveryCapabilityAndVerifiesArchive(t *testing.T) {
	kit, hostSigner, clientPublic, listener, dump := recoveryDownloadFixture(t, 2)
	serverDone := make(chan error, 1)
	go func() {
		for i := 0; i < 1+len(kit.Manifest.Archives); i++ {
			raw, err := listener.Accept()
			if err != nil {
				serverDone <- err
				return
			}
			if err := serveRecoveryTestSSH(raw, hostSigner, clientPublic, kit.Manifest, dump); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	output := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths, err := Download(ctx, kit, output, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(kit.Manifest.Archives) {
		t.Fatalf("paths=%v", paths)
	}
	for i, path := range paths {
		if path != filepath.Join(output, kit.Manifest.Archives[i].RepoID+".svndump") {
			t.Fatalf("path=%s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != string(dump) {
			t.Fatalf("download=%q err=%v", raw, err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func recoveryDownloadFixture(t *testing.T, count int) (Kit, ssh.Signer, ssh.PublicKey, net.Listener, []byte) {
	t.Helper()
	dump := []byte("SVN-fs-dump-format-version: 3\n")
	digest := sha256.Sum256(dump)
	now := time.Now().UTC()
	operationID, realmID := uuid.NewString(), uuid.NewString()
	manifest := repoworker.RecoveryManifest{
		Schema: repoworker.RecoveryManifestSchema, OperationID: operationID, RealmID: realmID,
		CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour),
	}
	for i := 0; i < count; i++ {
		manifest.Archives = append(manifest.Archives, repoworker.RecoveryArchive{
			ArchiveID: uuid.NewString(), RepoID: uuid.NewString(),
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(dump)),
		})
	}
	hostSigner := testRecoverySigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
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
	return kit, hostSigner, clientPublic, listener, dump
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
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
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
	served := false
	for channelRequest := range channels {
		// Deterministic model of a session slot not yet reclaimed by sshd.
		// Waiting or retrying on this connection can never pass the test.
		if served {
			_ = channelRequest.Reject(ssh.ConnectionFailed, "no more sessions")
			continue
		}
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
		default:
			found := false
			for _, archive := range manifest.Archives {
				if line == "get "+manifest.OperationID+" "+archive.ArchiveID {
					found = true
					_, _ = channel.Write(dump)
					break
				}
			}
			if !found {
				return errors.New("unexpected recovery request")
			}
		}
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, 0)
		_, _ = channel.SendRequest("exit-status", false, status)
		_ = channel.Close()
		served = true
	}
	return nil
}

func TestDownloadRepinsEveryConnectionAndKeepsVerifiedPartialResults(t *testing.T) {
	for _, test := range []struct {
		name          string
		badConnection int
	}{{"archive_host_changes", 1}, {"second_archive_host_changes", 2}} {
		t.Run(test.name, func(t *testing.T) {
			kit, host, key, listener, dump := recoveryDownloadFixture(t, 2)
			otherHost := testRecoverySigner(t)
			var workers sync.WaitGroup
			t.Cleanup(func() { listener.Close(); workers.Wait() })
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := 0; i <= test.badConnection; i++ {
					raw, e := listener.Accept()
					if e != nil {
						return
					}
					signer := host
					if i == test.badConnection {
						signer = otherHost
					}
					_ = serveRecoveryTestSSH(raw, signer, key, kit.Manifest, dump)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output := t.TempDir()
			paths, err := Download(ctx, kit, output, time.Now())
			if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
				t.Fatalf("error=%v", err)
			}
			if len(paths) != test.badConnection-1 {
				t.Fatalf("partial results=%v", paths)
			}
			files, e := os.ReadDir(output)
			if e != nil {
				t.Fatal(e)
			}
			if len(files) != len(paths) {
				t.Fatalf("unexpected partial/unverified files: %v", files)
			}
		})
	}
}

func TestRecoveryConnectionCancellationDuringHandshake(t *testing.T) {
	kit, _, _, listener, _ := recoveryDownloadFixture(t, 1)
	accepted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-accepted:
			cancel()
		case <-ctx.Done():
		}
	}()
	result := make(chan error, 1)
	output := t.TempDir()
	go func() {
		_, err := Download(ctx, kit, output, time.Now())
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled handshake succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handshake ignored cancellation")
	}
	<-done
}
