package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

// caseSiblingClient answers only the one question this guard asks. Embedding
// the interface keeps the double to the surface actually used: anything else
// would panic loudly rather than quietly returning a zero value.
type caseSiblingClient struct {
	client.Client
	versioned map[string]string
}

func (c *caseSiblingClient) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	out := make([]client.StatusEntry, 0, len(paths))
	for _, p := range paths {
		item, known := c.versioned[p]
		if !known {
			continue
		}
		out = append(out, client.StatusEntry{Path: p, Item: item})
	}
	return out, nil
}

// foldsCase reports whether dir's filesystem resolves a name differing only in
// case. Each half of the guard is meaningful on exactly one kind of filesystem,
// so both are measured here rather than assumed from the OS - the same reason
// the guard itself measures rather than checking runtime.GOOS.
func foldsCase(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "sonda-wielkosci")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	_, err := os.Stat(filepath.Join(dir, "SONDA-WIELKOSCI"))
	return err == nil
}

// A Windows client must never publish the deletion of a file it could not
// materialise. Measured 2026-09-08: a repository may legitimately hold Plik.txt
// and plik.txt, svn checkout reports success while writing one of them, and the
// other is missing from the first second. Treated as a deletion, this client
// removes another platform work without its user doing anything.
func TestAbsenceFromCaseCollisionIsNotADeletion(t *testing.T) {
	wc := t.TempDir()
	if !foldsCase(t, wc) {
		t.Skip("this filesystem distinguishes the two names, so the collision cannot arise here")
	}
	if err := os.WriteFile(filepath.Join(wc, "plik.txt"), []byte("wersja B"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Cli: &caseSiblingClient{versioned: map[string]string{"plik.txt": "normal"}}}

	reason := service.absenceExplainedByPlatform(context.Background(), wc, "Plik.txt", "missing")
	if reason == "" {
		t.Fatal("a versioned sibling differing only in case explains the absence and must block the deletion")
	}
}

// An unversioned sibling is deliberately not enough. On a case-insensitive
// filesystem that is indistinguishable from the user renaming Plik.txt to
// plik.txt, which is a real edit and must publish.
func TestAnUnversionedSiblingDoesNotBlockADeletion(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "plik.txt"), []byte("po zmianie nazwy"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Cli: &caseSiblingClient{versioned: map[string]string{"plik.txt": "unversioned"}}}

	if reason := service.absenceExplainedByPlatform(context.Background(), wc, "Plik.txt", "missing"); reason != "" {
		t.Fatalf("a rename must still publish: %s", reason)
	}
}

// An ordinary deletion has nothing occupying its place and must publish.
func TestAnOrdinaryDeletionStillPublishes(t *testing.T) {
	wc := t.TempDir()
	service := &Service{Cli: &caseSiblingClient{versioned: map[string]string{}}}

	if reason := service.absenceExplainedByPlatform(context.Background(), wc, "usuniety.txt", "missing"); reason != "" {
		t.Fatalf("a real deletion must not be withheld: %s", reason)
	}
}

// Only absence is examined. A path present and modified is not this guard business.
func TestOnlyMissingPathsAreExamined(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "plik.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Cli: &caseSiblingClient{versioned: map[string]string{"plik.txt": "normal"}}}

	if reason := service.absenceExplainedByPlatform(context.Background(), wc, "Plik.txt", "modified"); reason != "" {
		t.Fatalf("only a missing path may be withheld: %s", reason)
	}
}

// The mirror of the first case, and the reason the guard measures the
// filesystem rather than the operating system. Where names are distinguished,
// a repository holding Plik.txt and plik.txt puts both on disk, and removing
// one is an ordinary deletion. Withholding it would be permanent: nothing ever
// clears the condition, and the only trace is a warning in the log.
func TestOnACaseSensitiveFilesystemADeletionStillPublishes(t *testing.T) {
	wc := t.TempDir()
	if foldsCase(t, wc) {
		t.Skip("this filesystem folds case, so the deleted path still resolves")
	}
	if err := os.WriteFile(filepath.Join(wc, "plik.txt"), []byte("drugi dokument"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Cli: &caseSiblingClient{versioned: map[string]string{"plik.txt": "normal", "Plik.txt": "missing"}}}

	if reason := service.absenceExplainedByPlatform(context.Background(), wc, "Plik.txt", "missing"); reason != "" {
		t.Fatalf("a genuine deletion beside a case-differing sibling must publish: %s", reason)
	}
}
