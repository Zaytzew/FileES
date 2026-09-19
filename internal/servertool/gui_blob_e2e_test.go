//go:build !windows

package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/guiblob"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// Each control request starts the actual production repository worker afresh.
// SSH authentication is represented by its server-selected client ID; this
// harness does not replace the forced-command/SSH integration acceptance.
func TestGUIBlobWorkerProcess(t *testing.T) {
	if os.Getenv("FILEES_GUI_BLOB_WORKER") != "1" {
		t.Skip("worker child only")
	}
	os.Exit(runRepositoryWorker(os.Getenv("FILEES_GUI_BLOB_CONFIG"), []string{os.Getenv("FILEES_GUI_BLOB_CLIENT")}, os.Stdin, os.Stdout, os.Stderr))
}

type guiBlobProcessTransport struct{ config, client string }

func (s guiBlobProcessTransport) GUIBlob(ctx context.Context, _ string, write *guiblob.Write) (guiblob.State, error) {
	typ := control.TicketGetGUIBlob
	var payload any = struct{}{}
	if write != nil {
		typ = control.TicketSetGUIBlob
		payload = *write
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, s.client, payload, time.Now())
	if err != nil {
		return guiblob.State{}, err
	}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return guiblob.State{}, err
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGUIBlobWorkerProcess$")
	command.Env = append(os.Environ(), "FILEES_GUI_BLOB_WORKER=1", "FILEES_GUI_BLOB_CONFIG="+s.config, "FILEES_GUI_BLOB_CLIENT="+s.client)
	command.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return guiblob.State{}, fmt.Errorf("worker: %w: %s", err, stderr.String())
	}
	result, err := control.ParseResult(output)
	if err != nil {
		return guiblob.State{}, err
	}
	if result.Status != control.ResultOK {
		return guiblob.State{}, fmt.Errorf("worker rejected: %s", output)
	}
	var state guiblob.State
	if err = control.DecodeResultPayload(result.Result, &state); err == nil {
		err = state.Validate()
	}
	return state, err
}

func TestGUIBlobTwoDesktopIPCThroughWorkerProcesses(t *testing.T) {
	configPath, resultsRoot, _ := writeRepoPruneFixtureConfig(t)
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	realm, other := uuid.NewString(), uuid.NewString()
	a, b, c, phone := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, spec := range []struct{ id, realm, role string }{{a, realm, "normal"}, {b, realm, "normal"}, {c, other, "normal"}, {phone, realm, "ro"}} {
		view := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Drawer lab", ClientID: spec.id, RealmID: spec.realm, ClientRole: spec.role, Generation: 1, GeneratedAt: time.Now().UTC()}
		path := filepath.Join(config.Activation.ServiceWorkingCopy, "clients", spec.id, "view.json")
		if _, err := clientview.StoreIfNewer(path, view); err != nil {
			t.Fatal(err)
		}
	}
	runRepoPruneCommand(t, config.Activation.SVNBinary, "add", "--force", filepath.Join(config.Activation.ServiceWorkingCopy, "clients"))
	runRepoPruneCommand(t, config.Activation.SVNBinary, "commit", "-m", "drawer test clients", config.Activation.ServiceWorkingCopy)
	newDesktop := func(id, realm string) *ipcclient.Client {
		socketRoot, err := os.MkdirTemp("", "fd-ipc-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
		socket := filepath.Join(socketRoot, "d.sock")
		server := ipcserver.New(socket)
		server.RegisterActivation(contract.ActivationStatus{ServerID: "lab", ClientID: id, RealmID: realm, CanCreateRepositories: true})
		server.SetGUIBlobService(guiBlobProcessTransport{config: configPath, client: id})
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		if err := server.Start(ctx); err != nil {
			t.Fatal(err)
		}
		return ipcclient.New(socket, "drawer-test-gui")
	}
	desktopA, desktopB := newDesktop(a, realm), newDesktop(b, realm)
	initial, err := desktopA.GUIBlob(t.Context(), "lab", nil)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Version != "" || initial.Data != "" {
		t.Fatal(initial)
	}
	first := `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"one","name":"Żółć <A>"}],"repos":{}}`
	saved, err := desktopA.GUIBlob(t.Context(), "lab", &guiblob.Write{Data: first})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := desktopB.GUIBlob(t.Context(), "lab", nil)
	if err != nil || clone != saved {
		t.Fatal(clone, err)
	}
	foreign, err := newDesktop(c, other).GUIBlob(t.Context(), "lab", nil)
	if err != nil || foreign.Data != "" {
		t.Fatal(foreign, err)
	}
	if _, err := (guiBlobProcessTransport{config: configPath, client: phone}).GUIBlob(t.Context(), "lab", nil); err == nil {
		t.Fatal("phone read desktop state")
	}
	results := make(chan guiblob.State, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, client := range []*ipcclient.Client{desktopA, desktopB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := client.GUIBlob(t.Context(), "lab", &guiblob.Write{Expected: saved.Version, Data: fmt.Sprintf("opaque writer %d", i)})
			results <- state
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	conflicts := 0
	var winner guiblob.State
	for state := range results {
		if state.Conflict {
			conflicts++
		} else {
			winner = state
		}
	}
	if conflicts != 1 {
		t.Fatalf("concurrent process conflicts=%d", conflicts)
	}
	restarted, err := newDesktop(b, realm).GUIBlob(t.Context(), "lab", nil)
	if err != nil || restarted != winner {
		t.Fatal("daemon/worker restart", restarted, err)
	}
	info, err := os.Stat(filepath.Join(resultsRoot, "gui-blobs", realm+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("blob mode", info, err)
	}
	// Removing drawers is just a document update and cannot remove server views.
	empty := `{"schema":"filees.gui.drawers/v1","drawers":[],"repos":{}}`
	if _, err := desktopA.GUIBlob(t.Context(), "lab", &guiblob.Write{Expected: winner.Version, Data: empty}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientview.Load(filepath.Join(config.Activation.ServiceWorkingCopy, "clients", a, "view.json")); err != nil {
		t.Fatal(err)
	}
	t.Log("two IPC desktops, fresh worker per request, realm isolation, read-only denial, concurrent CAS, restart and 0600: PASS")
}

// Real OpenBSD kernel restrictions; the parent owns all scratch cleanup.
// This is the ResultsRoot permission used by client-entry, not a simulation
// of the entire SSH/set-id entry chain.
func TestGUIBlobSandboxStorage(t *testing.T) {
	root := os.Getenv("FILEES_GUI_BLOB_SANDBOX")
	if root == "" {
		root = t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "results"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "outside"), []byte("untouched"), 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGUIBlobSandboxStorage$", "-test.v")
		command.Env = append(os.Environ(), "FILEES_GUI_BLOB_SANDBOX="+root)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("sandbox child: %v: %s", err, out)
		}
		return
	}
	results := filepath.Join(root, "results")
	if err := obsandbox.Apply(obsandbox.Profile{Name: "gui-blob-native-test", Promises: writePromises, Paths: []obsandbox.Path{{Label: "repository-results", Name: results, Perms: "rwc"}}}); err != nil {
		t.Fatal(err)
	}
	store := repoworker.GUIBlobStore{Root: filepath.Join(results, "gui-blobs")}
	realm := uuid.NewString()
	saved, err := store.Exchange(t.Context(), realm, &guiblob.Write{Data: "drawer"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Exchange(t.Context(), realm, nil)
	if err != nil || read != saved {
		t.Fatal(read, err)
	}
	conflict, err := store.Exchange(t.Context(), realm, &guiblob.Write{Data: "stale"})
	if err != nil || !conflict.Conflict {
		t.Fatal(conflict, err)
	}
	if runtime.GOOS == "openbsd" {
		if _, err := os.ReadFile(filepath.Join(root, "outside")); err == nil {
			t.Fatal("unveil allowed unrelated file")
		}
	}
	if err := store.DeleteRealm(realm); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Exchange(t.Context(), realm, nil); err != nil || state.Data != "" {
		t.Fatal(state, err)
	}
}
