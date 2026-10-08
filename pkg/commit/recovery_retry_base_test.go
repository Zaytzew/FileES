package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
)

type deletionRecoveryClient struct {
	*conflictRecoveryClient
	base      int64
	baseErr   error
	rows      []client.StatusEntry
	inspected []string
}

func (c *deletionRecoveryClient) Revision(_ context.Context, target string) (int64, error) {
	c.inspected = append(c.inspected, target)
	return c.base, c.baseErr
}

func (c *deletionRecoveryClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	if c.rows != nil {
		return c.rows, nil
	}
	return []client.StatusEntry{{Path: "a.txt", Item: "deleted", Props: "none"}}, nil
}

func TestRecoveryOfStaleDeletionRequiresSeparateCanonicalDecision(t *testing.T) {
	for _, mode := range []string{"older", "unknown", "ahead", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			s, base, wc := conflictRecoveryFixture(t)
			c := &deletionRecoveryClient{conflictRecoveryClient: base, base: 3}
			s.Cli = c
			if mode == "unknown" {
				c.baseErr = errors.New("inspection unavailable")
			}
			if mode == "ahead" {
				c.base = 5
			}
			before, err := os.ReadFile(transactionPath(wc))
			if err != nil {
				t.Fatal(err)
			}
			accepted := false
			if mode != "unavailable" {
				s.PlanCanonicalRecovery = func(_ context.Context, transaction string, head int64) (*contract.CommitRecoveryPlan, error) {
					return &contract.CommitRecoveryPlan{PlanID: "canonical", RepoID: s.repoID, TransactionID: transaction, HeadRevision: head, Choice: contract.CommitRecoveryRebuild, Archive: "archive", ConflictCopy: "!kolizje/copy", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
				}
				s.AcceptCanonicalRecovery = func(context.Context, string) error { accepted = true; return nil }
			}
			plan, err := s.PlanCommitRecovery(t.Context())
			if mode == "unavailable" {
				if err == nil {
					t.Fatal("stale deletion offered a blind retry without canonical recovery")
				}
			} else {
				if err != nil || plan.Choice != contract.CommitRecoveryRebuild {
					t.Fatalf("stale deletion offered retry: %+v %v", plan, err)
				}
				result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
				if err != nil || result.State != "rebuild_requested" || !accepted {
					t.Fatal(result, err)
				}
			}
			after, _ := os.ReadFile(transactionPath(wc))
			if string(before) != string(after) || len(c.released) != 0 || c.resolves != 0 || c.mutations != 0 || !s.CommitRecoveryRequired() || len(s.staging) != 1 {
				t.Fatal("stale deletion retired old intent/writer/queue")
			}
		})
	}
}

func TestRecoveryRetryRechecksDeletionBaseBeforeReleasingWriter(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	c := &deletionRecoveryClient{conflictRecoveryClient: base, base: 4}
	s.Cli = c
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil || plan.Choice != contract.CommitRecoveryRetryQueue {
		t.Fatal(plan, err)
	}
	before, _ := os.ReadFile(transactionPath(wc))
	c.base = 3
	if _, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err == nil {
		t.Fatal("changed deletion base accepted")
	}
	after, _ := os.ReadFile(transactionPath(wc))
	if string(before) != string(after) || len(c.released) != 0 || !s.CommitRecoveryRequired() {
		t.Fatal("stale apply released HOLD")
	}
}

func TestRecoveryCurrentDeletionKeepsExistingRetry(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	c := &deletionRecoveryClient{conflictRecoveryClient: base, base: 4}
	s.Cli = c
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil || plan.Choice != contract.CommitRecoveryRetryQueue {
		t.Fatal(plan, err)
	}
	if _, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if len(c.released) != 1 || s.CommitRecoveryRequired() {
		t.Fatal("current deletion no longer retryable")
	}
	for _, target := range c.inspected {
		if target != filepath.Join(wc, "a.txt") {
			t.Fatal("inspected root instead of deleted target", target)
		}
	}
	if b, err := os.ReadFile(filepath.Join(wc, "a.txt")); err != nil || string(b) != "current edit" {
		t.Fatal("recovery changed user bytes", err)
	}
}

func TestRecoveryDeletionBaseRefusesAmbiguousTargets(t *testing.T) {
	for _, rows := range [][]client.StatusEntry{
		{{Path: "../foreign", Item: "deleted"}},
		{{Path: "other.txt", Item: "deleted"}},
		{{Path: "a.txt", Item: "deleted"}, {Path: "a.txt", Item: "deleted"}},
	} {
		s, base, wc := conflictRecoveryFixture(t)
		c := &deletionRecoveryClient{conflictRecoveryClient: base, base: 4, rows: rows}
		s.Cli = c
		if _, err := s.PlanCommitRecovery(t.Context()); err == nil {
			t.Fatal("ambiguous deletion offered retry", rows)
		}
		if len(c.released) != 0 || !s.CommitRecoveryRequired() {
			t.Fatal("refusal released HOLD")
		}
		if _, err := os.ReadFile(transactionPath(wc)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecoveryDeletionBaseChecksChildrenOfSelectedDirectory(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, _ := s.readIntent(wc)
	in.Paths = []string{"folder"}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	c := &deletionRecoveryClient{conflictRecoveryClient: base, base: 3, rows: []client.StatusEntry{{Path: "folder/child.txt", Item: "deleted"}}}
	s.Cli = c
	if _, err := s.PlanCommitRecovery(t.Context()); err == nil {
		t.Fatal("older child allowed blind retry")
	}
	if len(c.inspected) != 1 || c.inspected[0] != filepath.Join(wc, "folder", "child.txt") {
		t.Fatal("child base not inspected", c.inspected)
	}
}
