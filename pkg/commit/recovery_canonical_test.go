package commit

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"os"
	"testing"
	"time"
)

func TestCanonicalRecoveryApplyRefusesStaleDecisionWithoutMutations(t *testing.T) {
	for _, mode := range []string{"head-changed", "intent-changed", "expired", "wrong-choice", "accept-failed"} {
		t.Run(mode, func(t *testing.T) {
			s, base, wc := conflictRecoveryFixture(t)
			c := &writerRecoveryClient{conflictRecoveryClient: base, writer: "foreign-owner"}
			c.head = 264
			s.Cli = c
			in, _ := s.readIntent(wc)
			in.FirstRevision = 140
			if err := s.writeIntent(wc, in); err != nil {
				t.Fatal(err)
			}
			accepted := false
			s.PlanCanonicalRecovery = func(context.Context, string, int64) (*contract.CommitRecoveryPlan, error) {
				return &contract.CommitRecoveryPlan{PlanID: "opaque", RepoID: in.RepoID, TransactionID: in.ID, HeadRevision: 264, Choice: contract.CommitRecoveryRebuild, Archive: "archive", ConflictCopy: "!kolizje/conflicted-copy-opaque", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
			}
			s.AcceptCanonicalRecovery = func(context.Context, string) error { accepted = true; return errors.New("cannot persist decision") }
			plan, err := s.PlanCommitRecovery(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			choice := plan.Choice
			switch mode {
			case "head-changed":
				c.head++
			case "intent-changed":
				in.Paths = append(in.Paths, "another-file")
				if err := s.writeIntent(wc, in); err != nil {
					t.Fatal(err)
				}
			case "expired":
				s.commitRecoveryPlan.expires = time.Now().Add(-time.Second)
			case "wrong-choice":
				choice = contract.CommitRecoveryServerCopy
			}
			before, _ := os.ReadFile(transactionPath(wc))
			if _, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, choice); err == nil {
				t.Fatal("unsafe decision applied")
			}
			after, _ := os.ReadFile(transactionPath(wc))
			if accepted != (mode == "accept-failed") || string(before) != string(after) || len(c.released) != 0 || c.resolves != 0 || c.mutations != 0 || len(s.staging) != 1 {
				t.Fatal("stale decision changed old intent/writer/queue")
			}
		})
	}
}

func TestCanonicalRecoveryNeverRetiresUncertainIntentOrForeignWriter(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	c := &writerRecoveryClient{conflictRecoveryClient: base, writer: "older-owner-without-proof"}
	c.head = 264
	s.Cli = c
	in, _ := s.readIntent(wc)
	in.FirstRevision = 140
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(transactionPath(wc))
	accepted := false
	s.PlanCanonicalRecovery = func(context.Context, string, int64) (*contract.CommitRecoveryPlan, error) {
		return &contract.CommitRecoveryPlan{PlanID: "opaque", RepoID: in.RepoID, TransactionID: in.ID, HeadRevision: 264, Choice: contract.CommitRecoveryRebuild, Archive: "archive", ConflictCopy: "!kolizje/conflicted-copy-opaque", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
	}
	s.AcceptCanonicalRecovery = func(_ context.Context, id string) error { accepted = id == "opaque"; return nil }
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if plan.FirstRevision != 140 || plan.Choice != contract.CommitRecoveryRebuild {
		t.Fatal(plan)
	}
	result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
	if err != nil || result.State != "rebuild_requested" || !accepted {
		t.Fatal(result, err)
	}
	after, _ := os.ReadFile(transactionPath(wc))
	if string(before) != string(after) || c.writer != "older-owner-without-proof" || len(c.released) != 0 || c.resolves != 0 || c.mutations != 0 || !s.CommitRecoveryRequired() || len(s.staging) != 1 {
		t.Fatal("old proof/queue was retired instead of preserved")
	}
}
