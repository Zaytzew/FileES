package ipcserver

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"time"
)

func (rs *RepoState) SetIntentFuncs(plan func(context.Context) (*contract.IntentPlan, error), apply func(context.Context, string, string) (*contract.IntentApplyResult, error)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.intentPlanFn, rs.intentApplyFn = plan, apply
}

func (s *Server) handleRepoIntent(req contract.Request) contract.Response {
	rs := s.repoByID(req.RepoID)
	if req.RepoID == "" || rs == nil {
		return protoErr(req.RequestID, "proto.repo_not_found", nil)
	}
	rs.mu.RLock()
	allowed := rs.attached && rs.access == contract.AccessReadWrite && !rs.serverDeleted
	plan, apply := rs.intentPlanFn, rs.intentApplyFn
	rs.mu.RUnlock()
	var result any
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if !allowed || plan == nil || apply == nil {
		err = errors.New("intent resolution requires an attached writable working copy")
	} else if req.Command == contract.CmdRepoIntentPlan {
		result, err = plan(ctx)
	} else {
		var payload contract.IntentApplyPayload
		if contract.DecodePayload(req.Payload, &payload) != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		result, err = apply(ctx, payload.PlanID, payload.Choice)
	}
	if err != nil {
		return contract.ErrResponse(req.RequestID, "INTENT-1001", "ERROR", "REQUIRE_ACTION", "intent.resolution_refused", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}
