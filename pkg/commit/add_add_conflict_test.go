package commit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/client"
	"filees/pkg/errmap"
	"filees/pkg/talk"
)

// addAddClient behaves like Subversion 1.14 for a tree conflict: "theirs-full"
// is refused, and reverting a local addition over an incoming one leaves the
// incoming file.
type addAddClient struct {
	client.Client
	wc       string
	incoming map[string]string
	items    map[string]string // conflicted paths and their node status
	reverted []string
}

func (c *addAddClient) ConflictDetails(context.Context, string, string) ([]client.ConflictDetail, error) {
	return []client.ConflictDetail{{Type: "tree"}}, nil
}

func (c *addAddClient) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	if paths == nil {
		entries := []client.StatusEntry{{Path: "clean.txt", Item: "normal"}}
		for rel, item := range c.items {
			entries = append(entries, client.StatusEntry{Path: filepath.FromSlash(rel), Item: item, Conflicted: true})
		}
		return entries, nil
	}
	var entries []client.StatusEntry
	for _, rel := range paths {
		item, conflicted := c.items[rel]
		if !conflicted {
			item = "normal"
		}
		entries = append(entries, client.StatusEntry{Path: filepath.FromSlash(rel), Item: item, Conflicted: conflicted})
	}
	return entries, nil
}

func (c *addAddClient) Resolve(context.Context, string, []string, string) (string, error) {
	return "", errors.New("E155027: Tree conflict can only be resolved to 'working' or 'mine-conflict' state")
}

func (c *addAddClient) Revert(_ context.Context, _ string, paths []string) (string, error) {
	for _, rel := range paths {
		c.reverted = append(c.reverted, rel)
		if err := os.WriteFile(filepath.Join(c.wc, filepath.FromSlash(rel)), []byte(c.incoming[rel]), 0o644); err != nil {
			return "", err
		}
		delete(c.items, rel)
	}
	return "", nil
}

func addAddFixture(t *testing.T, local map[string]string, cli *addAddClient) (*Service, *bytes.Buffer, *int) {
	t.Helper()
	cli.wc = t.TempDir()
	for rel, text := range local {
		abs := filepath.Join(cli.wc, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var journal bytes.Buffer
	unresolved := -1
	s := &Service{Cli: cli, Logger: talk.With("add-add"), ErrSink: errmap.NewSink(&journal, "commit:test"), OnConflicts: func(n int) { unresolved = n }}
	return s, &journal, &unresolved
}

// Owner's KRAŃCOWA-PŁOŃSK, 2026-09-25: 93 files added here and brought by an
// update, all identical. Nothing is lost, so nothing is kept or reported.
func TestStandingAddAddConflictWithIdenticalFileResolvesSilently(t *testing.T) {
	cli := &addAddClient{incoming: map[string]string{"KRCPL_3d_Assets/3dSolid_48.udsmesh": "mesh"}, items: map[string]string{"KRCPL_3d_Assets/3dSolid_48.udsmesh": "replaced"}}
	s, journal, unresolved := addAddFixture(t, map[string]string{"KRCPL_3d_Assets/3dSolid_48.udsmesh": "mesh", "clean.txt": "x"}, cli)

	s.ReconcileStandingConflicts(t.Context(), cli.wc)

	if len(cli.reverted) != 1 || len(cli.items) != 0 {
		t.Fatalf("reverted=%v left=%v", cli.reverted, cli.items)
	}
	if _, err := os.Stat(filepath.Join(cli.wc, kolizjeDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an identical file left a copy in %s: %v", kolizjeDir, err)
	}
	if journal.Len() != 0 || *unresolved != 0 {
		t.Fatalf("journal=%q unresolved=%d", journal.String(), *unresolved)
	}
}

// Different bytes follow the existing policy: the server version wins and the
// local one is kept in !kolizje, reported once.
func TestAddAddConflictWithDifferentFileKeepsTheLocalCopy(t *testing.T) {
	cli := &addAddClient{incoming: map[string]string{"plan.dwg": "server"}, items: map[string]string{"plan.dwg": "replaced"}}
	s, journal, unresolved := addAddFixture(t, map[string]string{"plan.dwg": "local"}, cli)

	s.ReconcileUpdateConflicts(t.Context(), cli.wc, "   C plan.dwg\n")

	if got, _ := os.ReadFile(filepath.Join(cli.wc, "plan.dwg")); string(got) != "server" {
		t.Fatalf("working file = %q, want the server version", got)
	}
	copies, _ := filepath.Glob(filepath.Join(cli.wc, kolizjeDir, "*_lokalne", "plan.dwg"))
	if len(copies) != 1 {
		t.Fatalf("local copy not kept: %v", copies)
	}
	if kept, _ := os.ReadFile(copies[0]); string(kept) != "local" {
		t.Fatalf("kept copy = %q", kept)
	}
	if !strings.Contains(journal.String(), "recon.conflict") || *unresolved != 0 {
		t.Fatalf("journal=%q unresolved=%d", journal.String(), *unresolved)
	}
}

// Only a local addition over an incoming one is reverted. Any other tree
// conflict (a local edit over an incoming delete) stays for the user.
func TestOtherTreeConflictsAreNotReverted(t *testing.T) {
	cli := &addAddClient{incoming: map[string]string{}, items: map[string]string{"notes.txt": "added"}}
	s, _, unresolved := addAddFixture(t, map[string]string{"notes.txt": "edited"}, cli)

	s.ReconcileStandingConflicts(t.Context(), cli.wc)

	if len(cli.reverted) != 0 || *unresolved != 1 {
		t.Fatalf("reverted=%v unresolved=%d", cli.reverted, *unresolved)
	}
	if got, _ := os.ReadFile(filepath.Join(cli.wc, "notes.txt")); string(got) != "edited" {
		t.Fatalf("local edit changed: %q", got)
	}
}
