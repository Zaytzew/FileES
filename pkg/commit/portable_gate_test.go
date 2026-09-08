package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/portablepath"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

// statusClient answers one full-working-copy status and nothing else.
type statusClient struct {
	client.Client
	entries []client.StatusEntry
}

func (c *statusClient) Status(_ context.Context, _ string, _ []string) ([]client.StatusEntry, error) {
	return c.entries, nil
}

func TestOrdinaryNamesPassTheGate(t *testing.T) {
	wc := t.TempDir()
	for _, rel := range []string{"rysunek.dwg", "Umowa 2026-09.pdf", "kropka.w.srodku.txt"} {
		if problem := gateProblem(wc, rel); problem != nil {
			t.Errorf("gateProblem(%q) = %v, want accepted", rel, problem)
		}
	}
}

func TestReservedNameIsRefused(t *testing.T) {
	wc := t.TempDir()
	problem := gateProblem(wc, "projekt/CON.txt")
	if problem == nil || problem.Kind != portablepath.ReservedDevice {
		t.Fatalf("gateProblem = %v, want ReservedDevice", problem)
	}
}

// The name is fine alone; the sibling already on disk is what makes it
// impossible. This is the case that only a case-sensitive client can create,
// and the one the owner's message is written for.
func TestCollisionWithAnExistingSiblingIsRefused(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "Tekst.txt"), []byte("umowa"), 0o600); err != nil {
		t.Fatal(err)
	}
	problem := gateProblem(wc, "tekst.txt")
	if problem == nil || problem.Kind != portablepath.CaseCollision {
		t.Fatalf("gateProblem = %v, want CaseCollision", problem)
	}
	if problem.Detail != "Tekst.txt" {
		t.Fatalf("Detail = %q, want the colliding sibling named", problem.Detail)
	}
}

// A directory that cannot be read is not evidence of anything. Refusing here
// would turn one unreadable directory into a permanent block on every file in
// it.
func TestAnUnreadableDirectoryDoesNotBlock(t *testing.T) {
	wc := t.TempDir()
	if problem := gateProblem(wc, "nie/ma/takiego/katalogu/plik.txt"); problem != nil {
		t.Fatalf("gateProblem = %v, want accepted", problem)
	}
}

// Only new objects are gated. A path already under version control is past this
// question, and refusing its modifications would strand work FileES itself
// accepted earlier.
func TestOnlyNewObjectsAreGated(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "Tekst.txt"), []byte("umowa"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Logger: talk.With("portable-gate-test"), wc: wc}

	for _, op := range []watcher.OpType{watcher.Modified, watcher.Deleted} {
		if service.refuseUnportable(watcher.Event{Rel: "tekst.txt", Op: op}) {
			t.Errorf("op %v must not be gated", op)
		}
	}
	if !service.refuseUnportable(watcher.Event{Rel: "tekst.txt", Op: watcher.Added}) {
		t.Error("a new object with a colliding name must be refused")
	}
}

func TestUnportableNamesAreDerivedFromDisk(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "Tekst.txt"), []byte("umowa"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Cli: &statusClient{entries: []client.StatusEntry{
		{Path: "tekst.txt", Item: "unversioned"},   // collides with Tekst.txt
		{Path: "CON.txt", Item: "unversioned"},     // reserved device
		{Path: "rysunek.dwg", Item: "unversioned"}, // perfectly fine
		{Path: "Tekst.txt", Item: "normal"},        // versioned, not our question
		{Path: "rysunek.dwl", Item: "unversioned"}, // AutoCAD litter, ignored by policy
	}}}

	got, err := service.UnportableNames(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Rel != "tekst.txt" || got[1].Rel != "CON.txt" {
		t.Fatalf("unexpected entries: %+v", got)
	}
	if got[0].Reason == "" || got[1].Reason == "" {
		t.Fatal("every refusal must carry a reason a person can act on")
	}
}
