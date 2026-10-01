package commit

import (
	"context"
	"errors"
	"time"

	contract "filees/pkg/contract/v1"
)

// Caller holds wcOpMu. A no-effect refusal may offer a separate canonical
// replacement decision, but must never weaken the existing retry proof.
func (s *Service) planCanonicalRecovery(ctx context.Context, in *commitIntent, head int64, refusal error) (*contract.CommitRecoveryPlan, error) {
	if s.PlanCanonicalRecovery == nil || s.AcceptCanonicalRecovery == nil {
		return nil, refusal
	}
	plan, err := s.PlanCanonicalRecovery(ctx, in.ID, head)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.PlanID == "" || plan.RepoID != in.RepoID || plan.TransactionID != in.ID || plan.HeadRevision != head || plan.Choice != contract.CommitRecoveryRebuild || plan.Archive == "" || plan.ConflictCopy == "" {
		return nil, errors.New("invalid canonical recovery plan")
	}
	expires, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil || !expires.After(time.Now()) {
		return nil, errors.New("expired canonical recovery plan")
	}
	plan.FirstRevision = in.FirstRevision
	s.commitRecoveryPlan = &commitRecoveryPlanState{id: plan.PlanID, transactionID: in.ID, firstRevision: in.FirstRevision, headRevision: head, expires: expires, choice: plan.Choice, transactionProof: recoveryIntentDigest(in)}
	return plan, nil
}
