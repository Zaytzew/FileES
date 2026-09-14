package ipcserver

import (
	"context"
	"errors"
	"time"

	contract "filees/pkg/contract/v1"
)

func (rs *RepoState) SetCommitRecoveryFuncs(required func() bool, plan func(context.Context) (*contract.CommitRecoveryPlan, error), apply func(context.Context, string, string) (*contract.CommitRecoveryApplyResult, error)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.commitRecoveryRequiredFn, rs.commitRecoveryPlanFn, rs.commitRecoveryApplyFn = required, plan, apply
}

func (s *Server) handleRepoCommitRecovery(req contract.Request) contract.Response {
	rs := s.repoByID(req.RepoID)
	if req.RepoID == "" || rs == nil {
		return protoErr(req.RequestID, "proto.repo_not_found", nil)
	}
	rs.mu.RLock()
	allowed := rs.attached && rs.access == contract.AccessReadWrite && !rs.serverDeleted
	plan, apply := rs.commitRecoveryPlanFn, rs.commitRecoveryApplyFn
	rs.mu.RUnlock()
	if !allowed || plan == nil || apply == nil {
		return contract.ErrResponse(req.RequestID, "COMMIT-3100", "ERROR", "REQUIRE_ACTION", "commit.recovery_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var result any
	var err error
	switch req.Command {
	case contract.CmdRepoCommitRecoveryPlan:
		result, err = plan(ctx)
	case contract.CmdRepoCommitRecoveryApply:
		var payload contract.CommitRecoveryApplyPayload
		if contract.DecodePayload(req.Payload, &payload) != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		result, err = apply(ctx, payload.PlanID, payload.Choice)
	default:
		err = errors.New("unsupported commit recovery command")
	}
	if err != nil {
		return contract.ErrResponse(req.RequestID, "COMMIT-3100", "ERROR", "REQUIRE_ACTION", "commit.recovery_refused", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}
