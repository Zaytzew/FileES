package main

import (
	"context"
	"errors"
	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
)

type intentResolutionClient interface {
	RepoIntentPlan(context.Context, string) (*contract.IntentPlan, error)
	RepoIntentApply(context.Context, string, string, string) (*contract.IntentApplyResult, error)
}
type intentResolverAdapter struct{ client intentResolutionClient }

func (a intentResolverAdapter) PlanIntents(ctx context.Context, repoID string) (*actions.IntentResolutionPlan, error) {
	plan, err := a.client.RepoIntentPlan(ctx, repoID)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.RepoID != repoID {
		return nil, errors.New("invalid intent plan response")
	}
	result := &actions.IntentResolutionPlan{ID: plan.PlanID, RepoID: plan.RepoID, Choice: plan.Choice}
	for _, p := range plan.Paths {
		result.Paths = append(result.Paths, actions.IntentResolutionPath{Path: p.Path, Operation: p.Operation, Size: p.Size})
	}
	return result, nil
}
func (a intentResolverAdapter) ApplyIntents(ctx context.Context, repoID, planID, choice string) error {
	result, err := a.client.RepoIntentApply(ctx, repoID, planID, choice)
	if err != nil {
		return err
	}
	if result == nil || result.PlanID != planID || result.State != "queued" {
		return errors.New("invalid intent decision response")
	}
	return nil
}
