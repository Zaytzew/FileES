package androidbind

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/mobileworker"
	v1 "filees/pkg/mobile/v1"

	"golang.org/x/crypto/ssh"
)

// --- SVN + dispatcher fixture (mirrors pkg/mobileclient's own test fixture) ---

type fakeAuth struct {
	repoPath string
	gen      int64
}

func (f fakeAuth) Resolve(context.Context, string, string) (mobileworker.View, error) {
	return mobileworker.View{RepoPath: f.repoPath, Generation: f.gen, Access: "rw"}, nil
}

func (f fakeAuth) List(context.Context, string) (mobileworker.Projection, error) {
	return mobileworker.Projection{
		RealmID:    "5b2b2595-312c-4e8f-9407-148e2a174033",
		RealmAlias: "acme",
		Generation: f.gen,
		Repositories: []mobileworker.RepositoryGrant{{
			RepoID: "repo-1", DisplayName: "JANCZEWICE", Access: "rw", State: "active",
		}},
	}, nil
}

func requireSVN(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svn", "svnadmin", "svnlook"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, errb.String())
	}
}

func fileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func newSeededRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	run(t, "svnadmin", "create", repo)
	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "photos", "a.jpg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "svn", "import", "-q", seed, fileURL(repo), "-m", "seed import")
	return repo
}

func newDispatcher(repo string) mobileworker.Dispatcher {
	auth := fakeAuth{repoPath: repo, gen: 3}
	return mobileworker.Dispatcher{
		Browser:  mobileworker.Browser{Authority: auth, Reader: mobileworker.SVNReader{}},
		Appender: mobileworker.Appender{Authority: auth, Reader: mobileworker.SVNReader{}, Committer: mobileworker.SVNAppender{}, Ledger: mobileworker.Ledger{Dir: os.TempDir()}},
		ClientID: "client-1",
	}
}

// --- fake SSH server bridging real sessions into a real Dispatcher ---

func generateEd25519(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// startDispatcherServer accepts SSH connections until the test ends and
// serves each exec session through a real mobileworker.Dispatcher — i.e. the
// same server-side code path the sshd forced command will eventually drive,
// minus OpenSSH itself (Etap 4b, environmental).
func startDispatcherServer(t *testing.T, hostSigner ssh.Signer, acceptClientKey ssh.PublicKey, d mobileworker.Dispatcher) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if key.Type() != acceptClientKey.Type() || !bytes.Equal(key.Marshal(), acceptClientKey.Marshal()) {
				return nil, errUnauthorizedKey
			}
			return &ssh.Permissions{}, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, chans, reqs, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "only session channels")
						continue
					}
					channel, requests, err := newChannel.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							req.Reply(true, nil)
							status := uint32(0)
							if err := d.Serve(context.Background(), channel, channel); err != nil {
								status = 1
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	return listener.Addr().String()
}

var errUnauthorizedKey = &keyError{}

type keyError struct{}

func (*keyError) Error() string { return "androidbind test: unauthorized client key" }

// --- tests ---

func TestClientEndToEndRefreshAndUpload(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	hostSigner := generateEd25519(t)

	storeDir := t.TempDir()

	// The client generates its own identity on first use; read it back so the
	// fake server can be told which key to accept, mirroring how a real
	// server is told the pinned client key during onboarding.
	probe, err := NewClient(storeDir, "unused:0", "filees-mobile-v1", string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))
	if err != nil {
		t.Fatal(err)
	}
	clientPubLine := probe.PublicKey()
	clientPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(clientPubLine))
	if err != nil {
		t.Fatal(err)
	}

	addr := startDispatcherServer(t, hostSigner, clientPub, newDispatcher(repo))

	client, err := NewClient(storeDir, addr, "filees-mobile-v1", string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))
	if err != nil {
		t.Fatal(err)
	}
	if client.PublicKey() != clientPubLine {
		t.Fatalf("identity did not persist: %q != %q", client.PublicKey(), clientPubLine)
	}

	listJSON, err := client.ListRepositoriesJSON()
	if err != nil {
		t.Fatal(err)
	}
	var projection v1.ListRepositoriesResult
	if err := json.Unmarshal([]byte(listJSON), &projection); err != nil {
		t.Fatalf("decode projection: %v (json=%s)", err, listJSON)
	}
	if projection.RealmAlias != "acme" || len(projection.Repositories) != 1 || projection.Repositories[0].DisplayName != "JANCZEWICE" {
		t.Fatalf("projection = %+v", projection)
	}

	manifestJSON, err := client.RefreshJSON("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	var manifest v1.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		t.Fatalf("decode manifest: %v (json=%s)", err, manifestJSON)
	}
	if manifest.ViewGeneration != 3 || manifest.RepoRevision != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}

	id, err := client.EnqueueUpload("repo-1", "photos", "new.txt", "text/plain", []byte("brand new"))
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty upload id")
	}

	uploadsJSON, err := client.ListUploadsJSON("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if uploadsJSON == "[]" || uploadsJSON == "" {
		t.Fatalf("expected the queued item to show up, got %q", uploadsJSON)
	}

	drainJSON, err := client.DrainPendingJSON("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	var results []struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		Revision  int64  `json:"revision,omitempty"`
		FinalPath string `json:"final_path,omitempty"`
	}
	if err := json.Unmarshal([]byte(drainJSON), &results); err != nil {
		t.Fatalf("decode drain results: %v (json=%s)", err, drainJSON)
	}
	if len(results) != 1 || results[0].ID != id || results[0].State != "committed" {
		t.Fatalf("drain results = %+v", results)
	}
	if results[0].FinalPath != "photos/new.txt" {
		t.Fatalf("final_path = %q", results[0].FinalPath)
	}
}

func TestListUploadsJSONEmptyRepoIsArray(t *testing.T) {
	client, err := NewClient(t.TempDir(), "unused:0", "filees-mobile-v1", string(ssh.MarshalAuthorizedKey(generateEd25519(t).PublicKey())))
	if err != nil {
		t.Fatal(err)
	}
	listJSON, err := client.ListUploadsJSON("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if listJSON != "[]" {
		t.Fatalf("empty queue must be a JSON array, got %q", listJSON)
	}
}

func TestClientDiscardUpload(t *testing.T) {
	client, err := NewClient(t.TempDir(), "unused:0", "filees-mobile-v1", string(ssh.MarshalAuthorizedKey(generateEd25519(t).PublicKey())))
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.EnqueueUpload("repo-1", "photos", "x.bin", "", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DiscardUpload("repo-1", id); err != nil {
		t.Fatal(err)
	}
	listJSON, err := client.ListUploadsJSON("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if listJSON != "[]" {
		t.Fatalf("expected empty list after discard, got %q", listJSON)
	}
}

func TestNewClientPersistsIdentityAcrossInstances(t *testing.T) {
	storeDir := t.TempDir()
	hostKey := string(ssh.MarshalAuthorizedKey(generateEd25519(t).PublicKey()))

	first, err := NewClient(storeDir, "unused:0", "u", hostKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(storeDir, "unused:0", "u", hostKey)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey() != second.PublicKey() {
		t.Fatalf("identity not stable across instances: %q != %q", first.PublicKey(), second.PublicKey())
	}
}

func TestNewClientRejectsEmptyStoreDir(t *testing.T) {
	if _, err := NewClient("", "addr:0", "u", "ssh-ed25519 AAAA"); err == nil {
		t.Fatal("expected error for empty store_dir")
	}
}
