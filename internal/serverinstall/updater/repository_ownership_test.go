//go:build !windows

package updater

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/serverinstall/platform"
)

// pathOwnership answers per path and records every change.
type pathOwnership struct {
	owners  map[string]platform.Ownership
	applied map[string]platform.Ownership
}

func (p *pathOwnership) Resolve(string, string) (platform.Ownership, error) {
	return platform.Ownership{}, nil
}
func (p *pathOwnership) Stat(path string) (platform.Ownership, error) {
	if owner, ok := p.owners[filepath.Clean(path)]; ok {
		return owner, nil
	}
	return platform.Ownership{UID: 1006, GID: 0}, nil
}
func (p *pathOwnership) Apply(path string, ownership platform.Ownership) error {
	p.applied[filepath.Clean(path)] = ownership
	p.owners[filepath.Clean(path)] = ownership
	return nil
}

// spot, 2026-09-25: db/rep-cache.db and its journal owned by root stopped
// every deletion of that repository until the owner chowned them by hand.
func TestInstallerGivesForeignOwnedRepositoryFilesBackToTheWorker(t *testing.T) {
	r, root := testRunner(t)
	repos := filepath.Join(root, "repositories")
	repo := filepath.Join(repos, "40485a49-0000-0000-0000-000000000000")
	for _, dir := range []string{filepath.Join(repo, "db", "revs", "0")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cache, journal, rev := filepath.Join(repo, "db", "rep-cache.db"), filepath.Join(repo, "db", "rep-cache.db-journal"), filepath.Join(repo, "db", "revs", "0", "1")
	for _, file := range []string{cache, journal, rev} {
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(r.Config.SysconfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Config.SysconfDir, "server.json"), []byte(`{"repositories":{"root":"`+filepath.ToSlash(repos)+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	owners := &pathOwnership{applied: map[string]platform.Ownership{}, owners: map[string]platform.Ownership{
		filepath.Clean(repos): {UID: 1006, GID: 0},
		cache:                 {UID: 0, GID: 0},
		journal:               {UID: 0, GID: 7},
	}}
	var out bytes.Buffer
	r.Ownership, r.Out = owners, &out
	root2, err := r.repositoriesRoot()
	if err != nil || root2 != filepath.Clean(filepath.FromSlash(filepath.ToSlash(repos))) {
		t.Fatalf("repositories root = %q, %v", root2, err)
	}

	if err := r.correctRepositoryOwnership(root2, true); err != nil {
		t.Fatal(err)
	}
	if len(owners.applied) != 0 || !strings.Contains(out.String(), "would correct 2 of") {
		t.Fatalf("dry run changed or misreported: applied=%v out=%s", owners.applied, out.String())
	}

	out.Reset()
	if err := r.correctRepositoryOwnership(root2, false); err != nil {
		t.Fatal(err)
	}
	if len(owners.applied) != 2 || owners.applied[cache] != (platform.Ownership{UID: 1006, GID: 0}) || owners.applied[journal] != (platform.Ownership{UID: 1006, GID: 7}) {
		t.Fatalf("applied = %v (only the user changes, the group stays)", owners.applied)
	}
	if !strings.Contains(out.String(), "rep-cache.db: uid 0 -> 1006") || !strings.Contains(out.String(), "corrected 2 of") {
		t.Fatalf("changes not reported:\n%s", out.String())
	}

	out.Reset()
	owners.applied = map[string]platform.Ownership{}
	if err := r.correctRepositoryOwnership(root2, false); err != nil || len(owners.applied) != 0 || !strings.Contains(out.String(), "checked 8 entries") || !strings.Contains(out.String(), "all owned by uid 1006") {
		t.Fatalf("second pass: applied=%v out=%q err=%v", owners.applied, out.String(), err)
	}

	out.Reset()
	if err := r.correctRepositoryOwnership("", false); err != nil || !strings.Contains(out.String(), "not set") {
		t.Fatalf("unset root: out=%q err=%v", out.String(), err)
	}

	// A root-owned repositories root names no worker account to restore.
	owners.owners[filepath.Clean(repos)] = platform.Ownership{UID: 0, GID: 0}
	owners.owners[cache] = platform.Ownership{UID: 0, GID: 0}
	if err := r.correctRepositoryOwnership(root2, false); err != nil || len(owners.applied) != 0 || !strings.Contains(out.String(), "is owned by root") {
		t.Fatalf("root-owned root: applied=%v out=%q err=%v", owners.applied, out.String(), err)
	}
}
