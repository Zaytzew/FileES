//go:build openbsd

package servertool

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/serverconfig"
	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

func TestWhaleWorkerNativeSandboxPublishesAcrossSessions(t *testing.T) {
	if os.Getenv("FILEES_WHALE_WORKER_NATIVE_CHILD") == "direct" {
		os.Exit(runWhaleWorker(os.Getenv("FILEES_WHALE_CONFIG"), []string{os.Getenv("FILEES_WHALE_CLIENT")}, os.Stdin, os.Stdout, os.Stderr))
	}
	if os.Getenv("FILEES_WHALE_WORKER_NATIVE_CHILD") == "gated" {
		os.Exit(RunClientWhaleSessionChild([]string{os.Getenv("FILEES_WHALE_WORKER"), os.Getenv("FILEES_WHALE_CONFIG"), os.Getenv("FILEES_WHALE_CLIENT"), os.Getenv("FILEES_WHALE_NONCE")}, os.Stderr))
	}
	svn := requireWhaleTool(t, "svn")
	svnadmin := requireWhaleTool(t, "svnadmin")
	svnlook := requireWhaleTool(t, "svnlook")
	svnmucc := requireWhaleTool(t, "svnmucc")
	svnserve := requireWhaleTool(t, "svnserve")

	root := t.TempDir()
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	clientID, realmID, repoID, generationID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	repositoriesRoot := filepath.Join(root, "repositories")
	repository := filepath.Join(repositoriesRoot, repoID)
	resultsRoot := filepath.Join(root, "results")
	serviceWC := filepath.Join(root, "service-wc")
	serviceRepository := filepath.Join(root, "service-repository")
	for _, dir := range []string{repositoriesRoot, resultsRoot, serviceWC, serviceRepository, filepath.Join(serviceWC, "clients", clientID)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runWhaleNativeCommand(t, svnadmin, "create", repository)

	view := clientview.View{
		Schema: clientview.Schema, ClientID: clientID, RealmID: realmID, Generation: 1,
		GeneratedAt: time.Now().UTC(), ClientRole: "normal", ActiveOperations: []json.RawMessage{},
		Repositories: []clientview.Repository{{RepoID: repoID, DisplayName: "Whale native", URL: "svn+ssh://_filees-data@filees.test/" + repoID, Access: "rw", State: "active", OwnerRealmID: realmID}},
	}
	viewRaw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceWC, "clients", clientID, "view.json"), viewRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "server.json")
	config := serverconfig.File{
		Schema: serverconfig.Schema, Root: filepath.Join(root, "onboarding"), OTPPepperFile: filepath.Join(root, "pepper"),
		OperationTTL: "30m", OTPAttempts: 3, ReversePortFirst: 42000, ReversePortLast: 42000,
		Activation: serverconfig.ActivationFile{
			Root: filepath.Join(root, "activation"), SessionRoot: filepath.Join(root, "sessions"),
			AuthorizedKeysFile: filepath.Join(root, "activation", "authorized_keys"), AuthzFile: filepath.Join(root, "activation", "service.authz"),
			ServiceWorkingCopy: serviceWC, ServiceRepository: serviceRepository, RepositoryName: "filees-service",
			ClientEntryPath: testBinary, SVNBinary: svn, SVNServeBinary: svnserve,
		},
		Repositories: serverconfig.RepositoryFile{
			Root: repositoriesRoot, ResultsRoot: resultsRoot, DataAuthzFile: filepath.Join(root, "activation", "data.authz"),
			SVNAdminBinary: svnadmin, SVNLookBinary: svnlook, URLPrefix: "svn+ssh://_filees-data@filees.test/",
			DeletionArchiveRoot: filepath.Join(root, "deleted"), RecoveryAdminContact: "admin@example.test",
		},
		Invitation: serverconfig.InvitationFile{
			ServerID: "whale-native-test", ServerAddress: "filees.test:2222",
			KnownHost: "[filees.test]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		SMTP: serverconfig.SMTPFile{Address: "127.0.0.1:2525", ClientName: "filees.test", From: "filees@example.test", MessageIDDomain: "filees.test", TLS: "none"},
	}
	// EffectiveSVNMuccBinary deliberately derives the sibling of svnadmin.
	if filepath.Clean(config.Repositories.EffectiveSVNMuccBinary()) != filepath.Clean(svnmucc) {
		t.Fatalf("svnmucc sibling mismatch: got %s want %s", config.Repositories.EffectiveSVNMuccBinary(), svnmucc)
	}
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	payload := []byte("native Whale payload")
	digest := sha256.Sum256(payload)
	identity := whale.Identity{LogicalRepoID: repoID, LogicalPath: "media/native.bin", GenerationID: generationID, ExpectedSize: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	window := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutWindow, Identity: identity, PayloadSize: int64(len(payload))}
	windowInput := whaleNativeFrame(t, window, payload)
	windowOutput := runWhaleWorkerNativeChild(t, configPath, clientID, windowInput)
	reader := bufio.NewReader(bytes.NewReader(windowOutput))
	ready := readWhaleNativeResponse(t, reader)
	ack := readWhaleNativeResponse(t, reader)
	if ready.Status != "continue" || ready.Result == nil || ready.Result.Offset != 0 {
		t.Fatalf("ready response = %+v", ready)
	}
	if ack.Status != "ok" || ack.Result == nil || ack.Result.Offset != int64(len(payload)) || ack.Result.State != whale.StateCommitting {
		t.Fatalf("window ack = %+v", ack)
	}
	if trailing, err := io.ReadAll(reader); err != nil || len(trailing) != 0 {
		t.Fatalf("window response trailing bytes=%q err=%v", trailing, err)
	}

	commit := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutCommit, Identity: identity}
	commitOutput := runWhaleWorkerNativeChild(t, configPath, clientID, whaleNativeFrame(t, commit, nil))
	published := readWhaleNativeResponse(t, bufio.NewReader(bytes.NewReader(commitOutput)))
	if published.Status != "ok" || published.Result == nil || published.Result.State != whale.StatePublished || published.Result.Revision != 1 {
		t.Fatalf("commit response = %+v", published)
	}
	raw := runWhaleNativeCommand(t, svnlook, "cat", repository, ".filees-whales/media/native.bin")
	if !bytes.Equal(raw, payload) {
		t.Fatalf("published payload = %q want %q", raw, payload)
	}
	// Discovery is relative to a logical snapshot, not necessarily to the
	// repository revision which published the Whale itself.
	runWhaleNativeCommand(t, svnmucc, "--non-interactive", "-m", "unrelated r2", "mkdir", "file://"+repository+"/ordinary")

	// A quote is metadata-only: it proves the immutable revision tuple but
	// does not create seekable cache state.
	discover := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetDiscover, Identity: whale.Identity{LogicalRepoID: repoID, LogicalPath: identity.LogicalPath}, Revision: 2}
	discovered := readWhaleNativeResponse(t, bufio.NewReader(bytes.NewReader(runWhaleWorkerNativeChild(t, configPath, clientID, whaleNativeFrame(t, discover, nil)))))
	if discovered.Status != "ok" || discovered.Result.Identity == nil || *discovered.Result.Identity != identity || discovered.Result.Revision != 1 {
		t.Fatalf("GET discovery = %+v", discovered)
	}
	quote := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetQuote, Identity: identity, Revision: 1}
	quoted := readWhaleNativeResponse(t, bufio.NewReader(bytes.NewReader(runWhaleWorkerNativeChild(t, configPath, clientID, whaleNativeFrame(t, quote, nil)))))
	if quoted.Status != "ok" || quoted.Result.State != whale.StateAwaitingConfirmation || quoted.Result.ExpectedSize != int64(len(payload)) {
		t.Fatalf("GET quote = %+v", quoted)
	}
	getCacheRoot := filepath.Join(resultsRoot, "whale", "get-cache")
	if entries, err := os.ReadDir(getCacheRoot); err == nil && len(entries) != 0 {
		t.Fatalf("quote created GET cache: %v", entries)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	transferID, confirmation := uuid.NewString(), uuid.NewString()
	readWindow := func(offset, count int64) []byte {
		t.Helper()
		request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetWindow, Identity: identity, Revision: 1, TransferID: transferID, ConfirmationToken: confirmation, Offset: offset, PayloadSize: count}
		output := runWhaleWorkerNativeChild(t, configPath, clientID, whaleNativeFrame(t, request, nil))
		reader := bufio.NewReader(bytes.NewReader(output))
		response := readWhaleNativeResponse(t, reader)
		if response.Status != "ok" || response.Result.Offset != offset || response.Result.PayloadSize != count {
			t.Fatalf("GET window response = %+v", response)
		}
		window, err := io.ReadAll(reader)
		if err != nil || int64(len(window)) != count {
			t.Fatalf("GET window bytes=%d err=%v", len(window), err)
		}
		return window
	}
	first := readWindow(0, 7)
	cachePath := filepath.Join(getCacheRoot, transferID, "payload.ready")
	cacheBefore, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	second := readWindow(7, int64(len(payload))-7)
	cacheAfter, err := os.Stat(cachePath)
	if err != nil || !cacheAfter.ModTime().Equal(cacheBefore.ModTime()) {
		t.Fatalf("GET resume rematerialized cache: before=%v after=%v err=%v", cacheBefore.ModTime(), cacheAfter.ModTime(), err)
	}
	if combined := append(first, second...); !bytes.Equal(combined, payload) {
		t.Fatalf("resumed GET payload = %q want %q", combined, payload)
	}
	release := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetRelease, Identity: identity, Revision: 1, TransferID: transferID, ConfirmationToken: confirmation}
	released := readWhaleNativeResponse(t, bufio.NewReader(bytes.NewReader(runWhaleWorkerNativeChild(t, configPath, clientID, whaleNativeFrame(t, release, nil)))))
	if released.Status != "ok" || released.Result.State != whale.StateLocal {
		t.Fatalf("GET release = %+v", released)
	}
	if _, err := os.Stat(filepath.Join(getCacheRoot, transferID)); !os.IsNotExist(err) {
		t.Fatalf("released GET cache still exists: %v", err)
	}
}

// TestWhaleSSHFixtureSetup creates only an isolated repository/config/view
// tree for the opt-in desktop Transport E2E. The caller owns and removes Root.
func TestWhaleSSHFixtureSetup(t *testing.T) {
	root := os.Getenv("FILEES_WHALE_FIXTURE_ROOT")
	clientID := os.Getenv("FILEES_WHALE_FIXTURE_CLIENT")
	repoID := os.Getenv("FILEES_WHALE_FIXTURE_REPO")
	if root == "" || clientID == "" || repoID == "" {
		t.Skip("external SSH fixture is not requested")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("fixture root must be absolute")
	}
	svn := requireWhaleTool(t, "svn")
	svnadmin := requireWhaleTool(t, "svnadmin")
	svnlook := requireWhaleTool(t, "svnlook")
	svnserve := requireWhaleTool(t, "svnserve")
	repositoriesRoot := filepath.Join(root, "repositories")
	repository := filepath.Join(repositoriesRoot, repoID)
	resultsRoot := filepath.Join(root, "results")
	serviceWC := filepath.Join(root, "service-wc")
	serviceRepository := filepath.Join(root, "service-repository")
	for _, dir := range []string{repositoriesRoot, resultsRoot, serviceWC, serviceRepository, filepath.Join(serviceWC, "clients", clientID)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runWhaleNativeCommand(t, svnadmin, "create", repository)
	realmID := uuid.NewString()
	view := clientview.View{Schema: clientview.Schema, ClientID: clientID, RealmID: realmID, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", ActiveOperations: []json.RawMessage{}, Repositories: []clientview.Repository{{RepoID: repoID, DisplayName: "SSH E2E", URL: "svn+ssh://_filees-data@filees.test/" + repoID, Access: "rw", State: "active", OwnerRealmID: realmID}}}
	viewRaw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceWC, "clients", clientID, "view.json"), viewRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	testBinary, _ := filepath.Abs(os.Args[0])
	config := serverconfig.File{
		Schema: serverconfig.Schema, Root: filepath.Join(root, "onboarding"), OTPPepperFile: filepath.Join(root, "pepper"), OperationTTL: "30m", OTPAttempts: 3, ReversePortFirst: 42000, ReversePortLast: 42000,
		Activation:   serverconfig.ActivationFile{Root: filepath.Join(root, "activation"), SessionRoot: filepath.Join(root, "sessions"), AuthorizedKeysFile: filepath.Join(root, "activation", "authorized_keys"), AuthzFile: filepath.Join(root, "activation", "service.authz"), ServiceWorkingCopy: serviceWC, ServiceRepository: serviceRepository, RepositoryName: "filees-service", ClientEntryPath: testBinary, SVNBinary: svn, SVNServeBinary: svnserve},
		Repositories: serverconfig.RepositoryFile{Root: repositoriesRoot, ResultsRoot: resultsRoot, DataAuthzFile: filepath.Join(root, "activation", "data.authz"), SVNAdminBinary: svnadmin, SVNLookBinary: svnlook, URLPrefix: "svn+ssh://_filees-data@filees.test/", DeletionArchiveRoot: filepath.Join(root, "deleted"), RecoveryAdminContact: "admin@example.test"},
		Invitation:   serverconfig.InvitationFile{ServerID: "whale-ssh-e2e", ServerAddress: "127.0.0.1", KnownHost: "[filees.test]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		SMTP:         serverconfig.SMTPFile{Address: "127.0.0.1:2525", ClientName: "filees.test", From: "filees@example.test", MessageIDDomain: "filees.test", TLS: "none"},
	}
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server.json"), configRaw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireWhaleTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s unavailable", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func runWhaleNativeCommand(t *testing.T, binary string, args ...string) []byte {
	t.Helper()
	output, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", binary, args, err, output)
	}
	return output
}

func whaleNativeFrame(t *testing.T, request whale.Request, payload []byte) []byte {
	t.Helper()
	header, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var framed bytes.Buffer
	if err := whale.WriteFrame(&framed, whale.RequestMagic, header); err != nil {
		t.Fatal(err)
	}
	framed.Write(payload)
	return framed.Bytes()
}

func runWhaleWorkerNativeChild(t *testing.T, configPath, clientID string, input []byte) []byte {
	t.Helper()
	workerPath := os.Getenv("FILEES_WHALE_NATIVE_WORKER")
	mode := "direct"
	nonce := ""
	var gateRead, gateWrite *os.File
	if workerPath != "" {
		if !filepath.IsAbs(workerPath) {
			t.Fatalf("FILEES_WHALE_NATIVE_WORKER must be absolute: %q", workerPath)
		}
		mode = "gated"
		nonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		var err error
		gateRead, gateWrite, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer gateRead.Close()
		defer gateWrite.Close()
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWhaleWorkerNativeSandboxPublishesAcrossSessions$")
	command.Env = []string{
		"FILEES_WHALE_WORKER_NATIVE_CHILD=" + mode,
		"FILEES_WHALE_WORKER=" + workerPath,
		"FILEES_WHALE_CONFIG=" + configPath,
		"FILEES_WHALE_CLIENT=" + clientID,
		"FILEES_WHALE_NONCE=" + nonce,
	}
	if gateRead != nil {
		command.ExtraFiles = []*os.File{gateRead}
	}
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start native Whale worker child: %v", err)
	}
	if gateRead != nil {
		_ = gateRead.Close()
		if _, err := io.WriteString(gateWrite, nonce); err != nil {
			_ = command.Process.Kill()
			t.Fatalf("write native Whale child gate: %v", err)
		}
		_ = gateWrite.Close()
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("native Whale worker child: %v: %s", err, stderr.String())
	}
	return stdout.Bytes()
}

func readWhaleNativeResponse(t *testing.T, reader *bufio.Reader) whale.Response {
	t.Helper()
	header, err := whale.ReadHeader(reader, whale.ResponseMagic)
	if err != nil {
		t.Fatal(err)
	}
	response, err := whale.ParseResponse(header)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
