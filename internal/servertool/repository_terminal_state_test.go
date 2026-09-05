package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStateEmitterTerminalWorkerHelper(t *testing.T) {
	if os.Getenv("FILEES_TERMINAL_HELPER") != "1" {
		t.Skip("subprocess only")
	}
	os.Exit(runReservationProjectionWorker(os.Getenv("FILEES_TERMINAL_CONFIG"), []string{os.Getenv("FILEES_TERMINAL_CLIENT")}, os.Stdin, os.Stdout, os.Stderr))
}

func TestStateEmitterDeleteReachesBothClients(t *testing.T) {
	configPath, _, _ := writeRepoPruneFixtureConfig(t)
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	root := config.Activation.ServiceWorkingCopy
	writeTestClientView(t, root, nil)
	view, err := clientview.Load(filepath.Join(root, "clients", testClientID, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	second := "77777777-7777-4777-8777-777777777777"
	for _, id := range []string{testClientID, second} {
		view.ClientID = id
		terminalJSON(t, filepath.Join(root, "clients", id, "view.json"), view)
		terminalJSON(t, filepath.Join(root, "admin", "clients", id+".json"), map[string]any{
			"schema": "filees.client-instance/v1", "client_id": id, "realm_id": view.RealmID, "state": "active",
		})
	}
	terminalJSON(t, filepath.Join(root, "admin", "realms", view.RealmID+".json"), map[string]any{
		"schema": "filees.realm/v1", "realm_id": view.RealmID, "state": "active", "created_at": time.Now(), "alias": "terminal-test",
	})
	publisher := repoworker.ServicePublisher{ServiceWC: root, DataAuthzFile: config.Repositories.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: root}}
	if err := publisher.Publish(context.Background(), repoID, view.RealmID, "Docs", "svn+ssh://_filees-client@lab/"+repoID, ""); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Activate(context.Background(), repoID, view.RealmID); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Delete(context.Background(), repoID, view.RealmID); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{testClientID, second} {
		current, err := clientview.Load(filepath.Join(root, "clients", id, "view.json"))
		if err != nil || len(current.Repositories) != 0 {
			t.Fatalf("withdrawal: %+v %v", current, err)
		}
		for _, schema := range []string{reservationv1.StateSchema, reservationv1.Schema} {
			req, _ := json.Marshal(reservationv1.Request{Schema: schema, RepoID: repoID})
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestStateEmitterTerminalWorkerHelper$")
			cmd.Env = append(os.Environ(), "FILEES_TERMINAL_HELPER=1", "FILEES_TERMINAL_CONFIG="+configPath, "FILEES_TERMINAL_CLIENT="+id)
			cmd.Stdin = bytes.NewReader(req)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			raw, err := cmd.Output()
			if schema == reservationv1.Schema {
				if err == nil || len(raw) != 0 {
					t.Fatal("v1 gained terminal access")
				}
				continue
			}
			if err != nil {
				t.Fatalf("emitter: %v %s", err, stderr.String())
			}
			result, err := reservationv1.ParseResult(raw)
			if err != nil || result.RepositoryState != "deleted" || result.ViewGeneration != current.Generation {
				t.Fatalf("terminal response: %s %v", raw, err)
			}
		}
	}
}

func terminalJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestStateEmitterHistoricalDeletionAuthorization(t *testing.T) {
	const owner = "33333333-3333-4333-8333-333333333333"
	const foreign = "44444444-4444-4444-8444-444444444444"
	root := t.TempDir()
	writeTestClientView(t, root, nil)
	req := reservationv1.Request{Schema: reservationv1.StateSchema, RepoID: repoID}
	path := filepath.Join(root, "admin", "repositories", repoID+".json")
	record := map[string]any{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": owner, "state": "deleted"}
	terminalJSON(t, path, record)
	view, deleted, err := authorizedStateView(root, testClientID, req)
	if err != nil || !deleted || view.Generation != 1 {
		t.Fatalf("historical owner: deleted=%v err=%v", deleted, err)
	}
	req.Schema = reservationv1.Schema
	if _, _, err := authorizedStateView(root, testClientID, req); err == nil {
		t.Fatal("legacy lock read accepted deleted repo")
	}
	req.Schema = reservationv1.StateSchema
	record["owner_realm_id"] = foreign
	terminalJSON(t, path, record)
	if _, _, err := authorizedStateView(root, testClientID, req); err == nil {
		t.Fatal("unknown realm learned deletion")
	}
	// A preserved historical grant allows only the terminal fact, not lock/data access.
	grant := repoworker.RealmGrantRecord{Schema: repoworker.RealmGrantSchema, RepoID: repoID, OwnerRealmID: foreign, RecipientRealmID: owner, State: "revoked", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	terminalJSON(t, filepath.Join(root, "admin", "grants", repoID, owner+".json"), grant)
	if _, deleted, err := authorizedStateView(root, testClientID, req); err != nil || !deleted {
		t.Fatalf("past grantee: %v %v", deleted, err)
	}
	record["state"] = "active"
	terminalJSON(t, path, record)
	if _, _, err := authorizedStateView(root, testClientID, req); err == nil {
		t.Fatal("revoked access inferred as deletion")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authorizedStateView(root, testClientID, req); err == nil {
		t.Fatal("missing record inferred as deletion")
	}
}
