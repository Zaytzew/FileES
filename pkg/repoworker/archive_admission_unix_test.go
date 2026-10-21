//go:build !windows

package repoworker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestArchiveAdmissionDurableBindingAndCorruption(t *testing.T) {
	f := ArchiveAdmissionFence{Root: t.TempDir()}
	if held, err := f.Held(); held || err != nil {
		t.Fatal(held, err)
	}
	repo, plan := uuid.NewString(), testArchivePolicy()
	if err := f.Hold(t.Context(), repo, plan); err != nil {
		t.Fatal(err)
	}
	other := plan
	other.Revision++
	if f.Open(t.Context(), repo, other) == nil || f.Hold(t.Context(), repo, other) == nil {
		t.Fatal("changed HEAD accepted")
	}
	other = plan
	other.RepositoryUUID = uuid.NewString()
	if f.Open(t.Context(), repo, other) == nil || f.Hold(t.Context(), repo, other) == nil {
		t.Fatal("changed UUID accepted")
	}
	if f.Hold(t.Context(), uuid.NewString(), plan) == nil {
		t.Fatal("other repo stole pending fence")
	}
	if held, err := (ArchiveAdmissionFence{Root: f.Root}).Held(); !held || err != nil {
		t.Fatal(held, err)
	}
	if err := f.Open(t.Context(), repo, plan); err != nil {
		t.Fatal(err)
	}
	if err := f.Open(t.Context(), repo, plan); err != nil {
		t.Fatal("lost ACK", err)
	}
	if err := os.Remove(filepath.Join(f.Root, "archive-admission", "state.json")); err != nil {
		t.Fatal(err)
	}
	if held, err := f.Held(); !held || err == nil {
		t.Fatal("missing required state reopened", held, err)
	}
	if f.Hold(t.Context(), repo, plan) == nil {
		t.Fatal("missing state silently recreated")
	}
}
