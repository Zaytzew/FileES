package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func init() {
	if handled, code := RunLockGuardMode(os.Args[0], os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
}

func TestAdminLockGuardsCanonicalScopeAndExplicitApply(t *testing.T) {
	root := t.TempDir()
	repoID, owner := uuid.NewString(), uuid.NewString()
	config := serverconfig.Config{Activation: activation.Config{ServiceWorkingCopy: filepath.Join(root, "service")}, Repositories: serverconfig.RepositoryFile{Root: filepath.Join(root, "repos")}}
	repo := filepath.Join(config.Repositories.Root, repoID)
	for _, p := range []string{filepath.Join(repo, "db"), filepath.Join(repo, "hooks"), filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "repositories")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "format"), []byte("5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	put := func(state string) {
		raw, _ := json.Marshal(map[string]string{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": owner, "state": state})
		if err := os.WriteFile(filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "repositories", repoID+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("active")
	states, err := repositoryLockGuards(config, repoID, owner, false, os.Args[0])
	if err != nil || len(states) != 2 || states[0].State != "missing" || states[1].State != "missing" {
		t.Fatalf("inspect: %+v %v", states, err)
	}
	if entries, err := os.ReadDir(filepath.Join(repo, "hooks")); err != nil || len(entries) != 0 {
		t.Fatal("inspect mutated hooks")
	}
	if _, err := repositoryLockGuards(config, repoID, uuid.NewString(), true, os.Args[0]); err == nil {
		t.Fatal("wrong owner allowed")
	}
	if _, err := repositoryLockGuards(config, "../repo", owner, true, os.Args[0]); err == nil {
		t.Fatal("escaped scope")
	}
	put("deleted")
	if _, err := repositoryLockGuards(config, repoID, owner, true, os.Args[0]); err == nil {
		t.Fatal("deleted repo allowed")
	}
	put("active")
	states, err = repositoryLockGuards(config, repoID, owner, true, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.State != "installed" {
			t.Fatal(states)
		}
	}
}

func TestAdminLockGuardsRequiresExplicitUUIDScope(t *testing.T) {
	var out, stderr bytes.Buffer
	code := RunAdmin([]string{"repo", "lock-guards", "--repo-id", "../repo"}, &out, &stderr)
	if code != ExitUsage || !bytes.Contains(stderr.Bytes(), []byte("--owner-realm-id")) {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
