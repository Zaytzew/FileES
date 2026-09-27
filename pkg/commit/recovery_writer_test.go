package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type writerRecoveryClient struct {
	*conflictRecoveryClient
	writer                 string
	inspectErr, releaseErr error
}

func (c *writerRecoveryClient) InspectCommitWriter(context.Context, string) (string, error) {
	return c.writer, c.inspectErr
}
func (c *writerRecoveryClient) ReleaseCommitWriter(ctx context.Context, wc, id string) error {
	if c.releaseErr != nil {
		return c.releaseErr
	}
	if c.writer != "" && c.writer != id {
		return errors.New("foreign writer")
	}
	c.writer = ""
	return c.transactionFake.ReleaseCommitWriter(ctx, wc, id)
}

func TestCommitRecoveryUnsupportedReleaseKeepsHold(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, _ := s.readIntent(wc)
	c := &writerRecoveryClient{conflictRecoveryClient: base, writer: in.ID, releaseErr: errors.ErrUnsupported}
	s.Cli = c
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err == nil {
		t.Fatal("unsupported writer release accepted")
	}
	got, _ := s.readIntent(wc)
	if got.Phase != "attempting" || c.resolves != 0 || c.writer != in.ID {
		t.Fatal("unsafe retirement", got)
	}
}

func TestDurableCommitForeignWriterDoesNotReplaceIntent(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, _ := s.readIntent(wc)
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(transactionPath(wc))
	c := &writerRecoveryClient{conflictRecoveryClient: base, writer: in.ID}
	s.Cli = c
	if err := s.commitDurable(t.Context(), wc, c, []string{"a.txt"}, "retry", "", nil); err == nil {
		t.Fatal("commit over pending writer accepted")
	}
	after, _ := os.ReadFile(transactionPath(wc))
	if string(before) != string(after) || c.mutations != 0 {
		t.Fatal("prior provenance overwritten")
	}
	if _, err := os.Stat(filepath.Join(wc, ".filees", "state", "commit.busy")); !os.IsNotExist(err) {
		t.Fatal("new busy marker created", err)
	}
}

func TestCommitRecoveryForeignWriterRequiresItsOwnProof(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "foreign-repo", "advanced", "changed-proof", "changed-current", "changed-writer", "busy"} {
		t.Run(mode, func(t *testing.T) {
			s, base, wc := conflictRecoveryFixture(t)
			in, _ := s.readIntent(wc)
			old := *in
			old.ID = "32ecf8db-f4ec-49ec-abfd-16cc78974a14"
			old.Phase = "done"
			previous := filepath.Join(wc, ".filees", "commit_cache", "previous-transaction.json")
			switch mode {
			case "foreign-repo":
				old.RepoURL = "file:///another"
			case "advanced":
				old.FirstRevision = 4
			}
			if mode != "missing" {
				if err := atomicWriteJSONSlice(previous, &old); err != nil {
					t.Fatal(err)
				}
			}
			c := &writerRecoveryClient{conflictRecoveryClient: base, writer: old.ID}
			s.Cli = c
			if mode == "busy" {
				c.inspectErr = errors.New("live native writer")
			}
			plan, err := s.PlanCommitRecovery(t.Context())
			if mode == "missing" || mode == "foreign-repo" || mode == "advanced" || mode == "busy" {
				if err == nil {
					t.Fatal("unproven old owner accepted")
				}
				if c.resolves != 0 || len(c.released) != 0 {
					t.Fatal("planning mutated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed-current" {
				in.RepoURL = "file:///other"
				in.Phase = "done"
				if err := s.writeIntent(wc, in); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-writer" {
				c.writer = in.ID
			}
			if mode == "changed-proof" {
				old.FirstRevision++
				if err := atomicWriteJSONSlice(previous, &old); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
			got, _ := s.readIntent(wc)
			if mode != "valid" {
				if err == nil || (got.Phase != "attempting" && mode != "changed-current") || c.resolves != 0 {
					t.Fatal("stale proof accepted", err)
				}
				return
			}
			if err != nil || got.Phase != "done" || len(c.released) != 1 || c.released[0] != old.ID || len(got.RecoveryWriterPlans) != 1 {
				t.Fatal("old owner not audited/released", got, err)
			}
		})
	}
}

func TestCommitRecoveryDoneIntentWithNativeOwnerIsVisible(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, _ := s.readIntent(wc)
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	c := &writerRecoveryClient{conflictRecoveryClient: base, writer: in.ID}
	s.Cli = c
	if _, err := s.recoverCommit(t.Context(), wc); err == nil {
		t.Fatal("orphan fence not held")
	}
	if !s.CommitRecoveryRequired() {
		t.Fatal("recovery action missing")
	}
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if s.CommitRecoveryRequired() {
		t.Fatal("recovery stayed active")
	}
}

func TestCommitNotSentWithSurvivingFenceKeepsIntent(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, _ := s.readIntent(wc)
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	c := &writerRecoveryClient{conflictRecoveryClient: base}
	s.Cli = c
	c.notSent = true
	c.before = func() { c.writer = "unreleased-owner" }
	if err := s.commitDurable(t.Context(), wc, c, []string{"a.txt"}, "unsent", "", nil); err == nil {
		t.Fatal("surviving fence ignored")
	}
	after, err := s.readIntent(wc)
	if err != nil || after.Phase != "attempting" || !s.CommitRecoveryRequired() {
		t.Fatal("provenance retired despite fence", after, err)
	}
}
