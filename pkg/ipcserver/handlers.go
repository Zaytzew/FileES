package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/passport"
	"filees/pkg/realmbranding"
	"filees/pkg/shout"
	"filees/pkg/talk"
)

// dispatch routes a validated request to the appropriate handler.
// Unknown commands return a structured error; the connection is not closed.
func (s *Server) dispatch(req contract.Request) contract.Response {
	switch req.Command {
	case contract.CmdSystemHello:
		return s.handleHello(req)
	case contract.CmdSystemStatus:
		return s.handleSystemStatus(req)
	case contract.CmdSystemRestart:
		return s.handleSystemLifecycle(req, true)
	case contract.CmdSystemShutdown:
		return s.handleSystemLifecycle(req, false)
	case contract.CmdUpdateStatus:
		return s.handleUpdateStatus(req)
	case contract.CmdUpdatePlan:
		return s.handleUpdatePlan(req)
	case contract.CmdUpdateApply:
		return s.handleUpdateApply(req)
	case contract.CmdActivationBegin:
		return s.handleActivationBegin(req)
	case contract.CmdActivationFinish:
		return s.handleActivationFinish(req)
	case contract.CmdActivationPending:
		return s.handleActivationPending(req)
	case contract.CmdActivationResume:
		return s.handleActivationResume(req)
	case contract.CmdRealmAliasClaim:
		return s.handleRealmAliasClaim(req)
	case contract.CmdRealmGrantRecipients:
		return s.handleRealmGrantRecipients(req)
	case contract.CmdRealmSetVisibility:
		return s.handleRealmSetVisibility(req)
	case contract.CmdRealmPublicBrandingGet:
		return s.handleRealmPublicBranding(req, false)
	case contract.CmdRealmPublicBrandingSet:
		return s.handleRealmPublicBranding(req, true)
	case contract.CmdServerDetach:
		return s.handleServerDetach(req)
	case contract.CmdServerSetSessionTimeout:
		return s.handleServerSetSessionTimeout(req)
	case contract.CmdRealmRemoveBegin:
		return s.handleRealmRemoveBegin(req)
	case contract.CmdRealmRemoveConfirm:
		return s.handleRealmRemoveConfirm(req)
	case contract.CmdRecoveryDownload:
		return s.handleRecoveryDownload(req)
	case contract.CmdMobilePairingBegin:
		return s.handleMobilePairingBegin(req)
	case contract.CmdRepoList:
		return s.handleRepoList(req)
	case contract.CmdRepoStatus:
		return s.handleRepoStatus(req)
	case contract.CmdRepoActivity:
		return s.handleRepoActivity(req)
	case contract.CmdRepoCreateRequest:
		return s.handleRepoCreateRequest(req)
	case contract.CmdRepoAttachIntent:
		return s.handleRepoAttachIntent(req)
	case contract.CmdRepoAttachApprove:
		return s.handleRepoAttachApprove(req)
	case contract.CmdRepoRelocate:
		return s.handleRepoRelocate(req)
	case contract.CmdRepoLocate:
		return s.handleRepoLocate(req)
	case contract.CmdRepoLoadDump:
		return s.handleRepoLoadDump(req)
	case contract.CmdRepoGrantAccess:
		return s.handleRepoGrantAccess(req, false)
	case contract.CmdRepoRevokeAccess:
		return s.handleRepoGrantAccess(req, true)
	case contract.CmdRepoSetEditingPolicy:
		return s.handleRepoSetEditingPolicy(req)
	case contract.CmdRepoPublicShareList:
		return s.handlePublicShare(req, "list")
	case contract.CmdRepoPublicShareCreate:
		return s.handlePublicShare(req, "create")
	case contract.CmdRepoPublicShareUpdate:
		return s.handlePublicShare(req, "update")
	case contract.CmdRepoPublicShareRevoke:
		return s.handlePublicShare(req, "revoke")
	case contract.CmdRepoPublicShareDelete:
		return s.handlePublicShare(req, "delete")
	case contract.CmdRepoDetach:
		return s.handleRepoDetach(req, false)
	case contract.CmdRepoDelete:
		return s.handleRepoDetach(req, true)
	case contract.CmdRepoLifecycleStatus:
		return s.handleRepoLifecycleStatus(req)
	case contract.CmdErrorList:
		return s.handleErrorList(req)
	case contract.CmdRepoLock:
		return s.handleRepoLockUnlock(req, true)
	case contract.CmdRepoUnlock:
		return s.handleRepoLockUnlock(req, false)
	case contract.CmdRepoReservationList:
		return s.handleRepoReservationList(req)
	case contract.CmdRepoReservationRelease:
		return s.handleRepoReservationRelease(req)
	case contract.CmdRepoPublish:
		return s.handleRepoPublish(req)
	case contract.CmdNoticeList:
		return s.handleNoticeList(req)
	case contract.CmdNoticeAck:
		return s.handleNoticeAck(req)
	default:
		return contract.ErrResponse(req.RequestID,
			"PROTO-0003", "ERROR", "NONE", "proto.unknown_command",
			map[string]string{"command": req.Command})
	}
}

func (s *Server) handleRecoveryDownload(req contract.Request) contract.Response {
	service := s.realmRemovalService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "RECOVERY-0001", "ERROR", "NONE", "recovery.unavailable", nil)
	}
	var payload contract.RecoveryDownloadPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.OperationID) == "" || !filepath.IsAbs(payload.OutputRoot) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	result, err := service.Download(ctx, payload)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "RECOVERY-1001", "ERROR", "REQUIRE_ACTION", "recovery.download_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRealmRemoveBegin(req contract.Request) contract.Response {
	service := s.realmRemovalService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REALM-0001", "ERROR", "NONE", "realm.remove_unavailable", nil)
	}
	var payload contract.RealmRemoveBeginPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || strings.TrimSpace(payload.NotificationEmail) == "" || !filepath.IsAbs(payload.RecoveryDirectory) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, active := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !active || activation.RealmID == "" {
		return contract.ErrResponse(req.RequestID, "SERVER-0002", "ERROR", "NONE", "server.not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := service.Begin(ctx, payload.ServerID, activation.RealmID, payload)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REALM-1001", "ERROR", "REQUIRE_ACTION", "realm.remove_begin_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRealmRemoveConfirm(req contract.Request) contract.Response {
	defer clear(req.Payload)
	service := s.realmRemovalService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REALM-0001", "ERROR", "NONE", "realm.remove_unavailable", nil)
	}
	var payload contract.RealmRemoveConfirmPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || strings.TrimSpace(payload.OperationID) == "" || len(payload.OTP) == 0 {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	defer clear(payload.OTP)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	result, err := service.Confirm(ctx, payload)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REALM-1002", "ERROR", "REQUIRE_ACTION", "realm.remove_confirm_failed", nil)
	}
	s.RemoveServer(payload.ServerID)
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleServerDetach(req contract.Request) contract.Response {
	service := s.serverDetachService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "SERVER-0001", "ERROR", "NONE", "server.detach_unavailable", nil)
	}
	var payload contract.ServerDetachPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	_, active := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !active {
		return contract.ErrResponse(req.RequestID, "SERVER-0002", "ERROR", "NONE", "server.not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if err := service.Detach(ctx, payload.ServerID); err != nil {
		return contract.ErrResponse(req.RequestID, "SERVER-1001", "ERROR", "REQUIRE_ACTION", "server.detach_failed", nil)
	}
	s.RemoveServer(payload.ServerID)
	return contract.OKResponse(req.RequestID, contract.ServerDetachResult{ServerID: payload.ServerID})
}

func (s *Server) handleServerSetSessionTimeout(req contract.Request) contract.Response {
	service := s.sessionTimeoutService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "SERVER-0003", "ERROR", "RETRY", "server.session_timeout_unavailable", nil)
	}
	var payload contract.ServerSetSessionTimeoutPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, active := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !active {
		return contract.ErrResponse(req.RequestID, "SERVER-0002", "ERROR", "NONE", "server.not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	minutes, err := service.SetSessionTimeout(ctx, payload.ServerID, payload.Minutes)
	if err != nil {
		if strings.Contains(err.Error(), "session timeout must be") {
			return contract.ErrResponse(req.RequestID, "SERVER-1002", "ERROR", "REQUIRE_ACTION", "server.session_timeout_invalid", nil)
		}
		talk.With("session-timeout:"+payload.ServerID).Warnf("save failed: %v", err)
		return contract.ErrResponse(req.RequestID, "SERVER-1003", "ERROR", "REQUIRE_ACTION", "server.session_timeout_failed", nil)
	}
	activation.SessionTimeoutMin = minutes
	s.RegisterActivation(activation)
	return contract.OKResponse(req.RequestID, contract.ServerSetSessionTimeoutResult{ServerID: payload.ServerID, Minutes: minutes})
}

func (s *Server) handleRepoReservationList(req contract.Request) contract.Response {
	var payload contract.RepoReservationListPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	repos := make([]*RepoState, 0, len(s.repos))
	for _, repo := range s.repos {
		if repo.ServerID() == payload.ServerID {
			repos = append(repos, repo)
		}
	}
	s.mu.RUnlock()
	if len(repos) == 0 {
		s.mu.RLock()
		_, activated := s.activations[payload.ServerID]
		s.mu.RUnlock()
		if !activated {
			return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
		}
		return contract.OKResponse(req.RequestID, contract.RepoReservationListResult{ServerID: payload.ServerID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result := contract.RepoReservationListResult{ServerID: payload.ServerID}
	for _, repo := range repos {
		rows, err := repo.ListReservations(ctx)
		if err != nil {
			return contract.ErrResponse(req.RequestID, "LOCK-2101", "ERROR", "RETRY", "reservation.list_failed", map[string]string{"repo_id": repo.Summary().ID, "detail": err.Error()})
		}
		result.Reservations = append(result.Reservations, rows...)
	}
	ownerIDs := make([]string, 0, len(result.Reservations))
	seenOwners := make(map[string]struct{}, len(result.Reservations))
	for _, row := range result.Reservations {
		if row.OwnerID == "" {
			continue
		}
		if _, seen := seenOwners[row.OwnerID]; !seen {
			seenOwners[row.OwnerID] = struct{}{}
			ownerIDs = append(ownerIDs, row.OwnerID)
		}
	}
	labels := map[string]string{}
	if resolver := s.ownerLabelResolver(); resolver != nil && len(ownerIDs) > 0 {
		// Owner labels improve presentation only; an unavailable control
		// worker must not hide otherwise authoritative reservation rows.
		if resolved, err := resolver.Resolve(ctx, payload.ServerID, ownerIDs); err == nil {
			labels = resolved
		}
	}
	for i := range result.Reservations {
		result.Reservations[i].OwnerLabel = labels[result.Reservations[i].OwnerID]
		result.Reservations[i].OwnerID = ""
	}
	sort.Slice(result.Reservations, func(i, j int) bool {
		left, right := result.Reservations[i], result.Reservations[j]
		if left.WorkingCopy != right.WorkingCopy {
			return left.WorkingCopy < right.WorkingCopy
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.RepoID < right.RepoID
	})
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRealmAliasClaim(req contract.Request) contract.Response {
	service := s.realmAliasService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REALM-0001", "ERROR", "RETRY", "realm.alias_unavailable", nil)
	}
	var payload contract.RealmAliasClaimPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || strings.TrimSpace(payload.Alias) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok {
		return contract.ErrResponse(req.RequestID, "REALM-0002", "ERROR", "NONE", "realm.not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alias, err := service.Claim(ctx, payload.ServerID, payload.Alias)
	if err != nil {
		// Keep the local IPC equally non-enumerating: callers are never told
		// whether a candidate exists or merely violates server policy.
		talk.With("realm-alias:"+payload.ServerID).Warnf("claim failed: %v", err)
		return contract.ErrResponse(req.RequestID, "REALM-1001", "ERROR", "REQUIRE_ACTION", "realm.alias_rejected", nil)
	}
	activation.RealmAlias = alias
	s.RegisterActivation(activation)
	return contract.OKResponse(req.RequestID, contract.RealmAliasClaimResult{Alias: alias})
}

func (s *Server) handleRealmGrantRecipients(req contract.Request) contract.Response {
	service := s.realmGrantService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "GRANT-0001", "ERROR", "RETRY", "realm.grants_unavailable", nil)
	}
	var payload contract.RealmGrantRecipientsPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "GRANT-2001", "ERROR", "NONE", "realm.grants_forbidden", nil)
	}
	if payload.RepoID != "" {
		repo := s.repoByID(payload.RepoID)
		if repo == nil || repo.ServerID() != payload.ServerID {
			return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
		}
		if activation.RealmID == "" || repo.Summary().OwnerRealmID != activation.RealmID {
			return contract.ErrResponse(req.RequestID, "GRANT-2001", "ERROR", "NONE", "realm.grants_forbidden", nil)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recipients, err := service.ListRecipients(ctx, payload.ServerID, payload.RepoID)
	if err != nil {
		talk.With("realm-grants:"+payload.ServerID).Warnf("recipient listing failed: %v", err)
		return contract.ErrResponse(req.RequestID, "GRANT-1001", "ERROR", "RETRY", "realm.grant_recipients_unavailable", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RealmGrantRecipientsResult{Recipients: recipients})
}

func (s *Server) handleRealmSetVisibility(req contract.Request) contract.Response {
	service := s.realmGrantService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "GRANT-0001", "ERROR", "RETRY", "realm.grants_unavailable", nil)
	}
	var payload contract.RealmSetVisibilityPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || (payload.Visibility != "hidden" && payload.Visibility != "listed") {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	_, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok {
		return contract.ErrResponse(req.RequestID, "REALM-0002", "ERROR", "NONE", "realm.not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	visibility, err := service.SetVisibility(ctx, payload.ServerID, payload.Visibility)
	if err != nil {
		talk.With("realm-grants:"+payload.ServerID).Warnf("directory visibility failed: %v", err)
		return contract.ErrResponse(req.RequestID, "GRANT-1003", "ERROR", "REQUIRE_ACTION", "realm.visibility_rejected", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RealmSetVisibilityResult{Visibility: visibility})
}

func (s *Server) handleRealmPublicBranding(req contract.Request, set bool) contract.Response {
	service := s.realmPublicBrandingService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "BRANDING-0001", "ERROR", "RETRY", "realm.branding_unavailable", nil)
	}
	var (
		serverID string
		branding realmbranding.Branding
	)
	if set {
		var payload contract.RealmPublicBrandingSetPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, branding = strings.TrimSpace(payload.ServerID), payload.Branding
		if normalized, err := realmbranding.Normalize(branding); err != nil || normalized != branding {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
	} else {
		var payload contract.RealmPublicBrandingGetPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID = strings.TrimSpace(payload.ServerID)
	}
	if serverID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[serverID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "BRANDING-2001", "ERROR", "NONE", "realm.branding_forbidden", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	if set {
		branding, err = service.SetPublicBranding(ctx, serverID, branding)
	} else {
		branding, err = service.GetPublicBranding(ctx, serverID)
	}
	if err != nil {
		talk.With("realm-branding:"+serverID).Warnf("public branding failed: %v", err)
		return contract.ErrResponse(req.RequestID, "BRANDING-1001", "ERROR", "REQUIRE_ACTION", "realm.branding_rejected", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RealmPublicBrandingResult{Branding: branding})
}

// handleRepoSetEditingPolicy forwards an owner's policy change to the server.
// The local checks here are a fast, honest refusal for cases the client can
// already see are hopeless - read-only role, or a repository this realm does
// not own. They are not the security boundary: the worker re-derives ownership
// from the authenticated session, so a client that lies still gets refused.
func (s *Server) handleRepoSetEditingPolicy(req contract.Request) contract.Response {
	service := s.editingPolicyService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "POLICY-0001", "ERROR", "RETRY", "repo.editing_policy_unavailable", nil)
	}
	var payload contract.RepoSetEditingPolicyPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	serverID, repoID := strings.TrimSpace(payload.ServerID), strings.TrimSpace(payload.RepoID)
	if serverID == "" || repoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if payload.Policy != "" && payload.Policy != "free" && payload.Policy != "lock_required" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(repoID)
	if rs == nil || rs.ServerID() != serverID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[serverID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || activation.RealmID == "" || rs.Summary().OwnerRealmID != activation.RealmID {
		return contract.ErrResponse(req.RequestID, "POLICY-2001", "ERROR", "NONE", "repo.editing_policy_forbidden", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	policy, err := service.SetEditingPolicy(ctx, serverID, repoID, payload.Policy)
	if err != nil {
		talk.With("editing-policy:"+serverID).Warnf("policy change failed: %v", err)
		return contract.ErrResponse(req.RequestID, "POLICY-2002", "ERROR", "RETRY", "repo.editing_policy_failed", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RepoSetEditingPolicyResult{RepoID: repoID, Policy: policy})
}

func (s *Server) handleRepoGrantAccess(req contract.Request, revoke bool) contract.Response {
	service := s.realmGrantService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "GRANT-0001", "ERROR", "RETRY", "realm.grants_unavailable", nil)
	}
	var serverID, repoID, recipientRealmID, access string
	if revoke {
		var payload contract.RepoRevokeAccessPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID, recipientRealmID = payload.ServerID, payload.RepoID, payload.RecipientRealmID
	} else {
		var payload contract.RepoGrantAccessPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID, recipientRealmID, access = payload.ServerID, payload.RepoID, payload.RecipientRealmID, payload.Access
	}
	if strings.TrimSpace(serverID) == "" || strings.TrimSpace(repoID) == "" || strings.TrimSpace(recipientRealmID) == "" || (!revoke && access != contract.AccessReadOnly && access != contract.AccessReadWrite) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(repoID)
	if rs == nil || rs.ServerID() != serverID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[serverID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories || activation.RealmID == "" || rs.Summary().OwnerRealmID != activation.RealmID || recipientRealmID == activation.RealmID {
		return contract.ErrResponse(req.RequestID, "GRANT-2001", "ERROR", "NONE", "realm.grants_forbidden", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var (
		result contract.RealmGrantResult
		err    error
	)
	if revoke {
		result, err = service.Revoke(ctx, serverID, repoID, recipientRealmID)
	} else {
		result, err = service.Grant(ctx, serverID, repoID, recipientRealmID, access)
	}
	if err != nil {
		talk.With("realm-grants:"+serverID).Warnf("grant mutation failed: %v", err)
		return contract.ErrResponse(req.RequestID, "GRANT-1002", "ERROR", "REQUIRE_ACTION", "realm.grant_rejected", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handlePublicShare(req contract.Request, action string) contract.Response {
	service := s.publicShareService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "SHARE-0001", "ERROR", "RETRY", "public_share.unavailable", nil)
	}
	var serverID, repoID, channelID string
	var declaration contract.PublicShareDeclaration
	keepPassword := false
	switch action {
	case "list":
		var payload contract.PublicShareListPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID = payload.ServerID, payload.RepoID
	case "create":
		var payload contract.PublicShareCreatePayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID, declaration = payload.ServerID, payload.RepoID, payload.PublicShareDeclaration
	case "update":
		var payload contract.PublicShareUpdatePayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID, channelID, declaration, keepPassword = payload.ServerID, payload.RepoID, payload.ChannelID, payload.PublicShareDeclaration, payload.KeepPassword
	case "revoke", "delete":
		var payload contract.PublicShareChannelPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		serverID, repoID, channelID = payload.ServerID, payload.RepoID, payload.ChannelID
	default:
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if strings.TrimSpace(serverID) == "" || strings.TrimSpace(repoID) == "" || (req.RepoID != "" && req.RepoID != repoID) || ((action == "update" || action == "revoke" || action == "delete") && strings.TrimSpace(channelID) == "") {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if (action == "create" || action == "update") && declaration.RepoID != repoID {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	repo := s.repoByID(repoID)
	if repo == nil || repo.ServerID() != serverID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[serverID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories || activation.RealmID == "" || repo.Summary().OwnerRealmID != activation.RealmID {
		return contract.ErrResponse(req.RequestID, "SHARE-2001", "ERROR", "NONE", "public_share.forbidden", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if action == "list" {
		shares, err := service.ListPublicShares(ctx, serverID, repoID)
		if err != nil {
			talk.With("public-shares:"+serverID).Warnf("channel listing failed: %v", err)
			return contract.ErrResponse(req.RequestID, "SHARE-1001", "ERROR", "RETRY", "public_share.list_failed", nil)
		}
		return contract.OKResponse(req.RequestID, contract.PublicShareListResult{Shares: shares})
	}
	var (
		result contract.PublicShareResult
		err    error
	)
	switch action {
	case "create":
		result, err = service.CreatePublicShare(ctx, serverID, declaration)
	case "update":
		result, err = service.UpdatePublicShare(ctx, serverID, channelID, declaration, keepPassword)
	case "revoke":
		result, err = service.RevokePublicShare(ctx, serverID, channelID)
	case "delete":
		result, err = service.DeletePublicShare(ctx, serverID, channelID)
	}
	if err != nil {
		talk.With("public-shares:"+serverID).Warnf("channel %s failed: %v", action, err)
		return contract.ErrResponse(req.RequestID, "SHARE-1002", "ERROR", "REQUIRE_ACTION", "public_share.rejected", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoReservationRelease(req contract.Request) contract.Response {
	var payload contract.RepoReservationReleasePayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || strings.TrimSpace(payload.RepoID) == "" || strings.TrimSpace(payload.Path) == "" || strings.TrimSpace(payload.ExpectedToken) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if filepath.IsAbs(payload.Path) || payload.Path == "." || strings.HasPrefix(filepath.Clean(payload.Path), ".."+string(filepath.Separator)) || filepath.Clean(payload.Path) == ".." {
		return contract.ErrResponse(req.RequestID, "LOCK-2102", "ERROR", "NONE", "reservation.invalid_path", nil)
	}
	repo := s.repoByID(payload.RepoID)
	if repo == nil || repo.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := repo.ReleaseReservation(ctx, payload.Path, payload.ExpectedToken, payload.ConfirmRisk); err != nil {
		return contract.ErrResponse(req.RequestID, "LOCK-2103", "ERROR", "REQUIRE_ACTION", "reservation.release_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.LockResult{Output: "released"})
}

func (s *Server) handleSystemLifecycle(req contract.Request, restart bool) contract.Response {
	if s.systemLifecycleService() == nil {
		return contract.ErrResponse(req.RequestID, "SYSTEM-0001", "ERROR", "NONE", "system.lifecycle_unavailable", nil)
	}
	action := "shutdown"
	if restart {
		action = "restart"
	}
	return contract.OKResponse(req.RequestID, contract.SystemLifecycleResult{Action: action})
}

func (s *Server) handleRepoDetach(req contract.Request, deleteRepository bool) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoDetachPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	summary := rs.Summary()
	if !summary.Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2006", "ERROR", "NONE", "repo.not_attached", nil)
	}
	if summary.AttachmentPolicy == "required" {
		return contract.ErrResponse(req.RequestID, "REPO-2010", "ERROR", "NONE", "repo.detach_required_forbidden", nil)
	}
	if deleteRepository {
		s.mu.RLock()
		activation, ok := s.activations[payload.ServerID]
		s.mu.RUnlock()
		if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories ||
			activation.RealmID == "" || summary.OwnerRealmID != activation.RealmID {
			return contract.ErrResponse(req.RequestID, "REPO-2011", "ERROR", "NONE", "repo.delete_forbidden", nil)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	result, err := service.BeginDetach(ctx, payload.ServerID, payload.RepoID, deleteRepository)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2012", "ERROR", "REQUIRE_ACTION", "repo.detach_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoActivity(req contract.Request) contract.Response {
	source := s.activitySource()
	if source == nil {
		return contract.ErrResponse(req.RequestID, "REPO-3001", "ERROR", "NONE", "repo.activity_unavailable", nil)
	}
	var payload contract.RepoActivityPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	limit := payload.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	entries := source.List()
	if len(entries) > limit {
		entries = entries[:limit]
	}
	result := make([]contract.ActivityRecord, len(entries))
	for i, entry := range entries {
		result[i] = contract.ActivityRecord{RepoID: entry.RepoID, Path: entry.Path, Kind: string(entry.Kind), Stage: string(entry.Stage), DetectedAt: entry.DetectedAt.Format(time.RFC3339Nano), UpdatedAt: entry.UpdatedAt.Format(time.RFC3339Nano), Revision: entry.Revision, ErrorID: entry.ErrorID}
	}
	return contract.OKResponse(req.RequestID, contract.RepoActivityResult{Entries: result})
}

func (s *Server) handleRepoRelocate(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoRelocatePayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	if !rs.Snapshot().Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2006", "ERROR", "NONE", "repo.not_attached", nil)
	}
	result, err := service.BeginRelocate(payload.ServerID, payload.RepoID, payload.NewLocalPath)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2007", "ERROR", "REQUIRE_ACTION", "repo.relocation_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoLocate(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoLocatePayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	if !rs.Snapshot().Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2006", "ERROR", "NONE", "repo.not_attached", nil)
	}
	result, err := service.BeginLocate(payload.ServerID, payload.RepoID, payload.ExistingLocalPath)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2010", "ERROR", "REQUIRE_ACTION", "repo.locate_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}

// handleRepoLoadDump triggers LOAD_REPOSITORY_DUMP for an already-attached
// repository (create + carrier commit already done through the normal repo
// flow). The server worker is the real authorization boundary
// (session.RealmID derived from the authenticated connection, never this
// payload) - the checks here are the same ergonomics gate every other
// repo-administration command already applies, not the enforcement itself.
func (s *Server) handleRepoLoadDump(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoLoadDumpPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	if !rs.Snapshot().Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2006", "ERROR", "NONE", "repo.not_attached", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "REPO-2013", "ERROR", "NONE", "repo.load_dump_forbidden", nil)
	}
	result, err := service.BeginLoadDump(payload.ServerID, payload.RepoID, payload.ApplyCurrentIgnorePolicy, payload.KeepLastRevisions)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2014", "ERROR", "REQUIRE_ACTION", "repo.load_dump_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoLifecycleStatus(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoLifecycleStatusPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	result, err := service.Status(payload.OperationID)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2008", "ERROR", "NONE", "repo.lifecycle_operation_not_found", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoAttachApprove(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoAttachApprovePayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	summary := rs.Summary()
	if summary.Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2003", "ERROR", "NONE", "repo.already_attached", nil)
	}
	if rs.ProjectedState() != contract.StateActive || (summary.Access != "r" && summary.Access != "rw") {
		return contract.ErrResponse(req.RequestID, "REPO-2004", "ERROR", "RETRY", "repo.not_attachable", nil)
	}
	result, err := service.ApproveAttach(payload.OperationID, payload.ServerID, payload.RepoID, summary.URL, summary.Access)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2005", "ERROR", "REQUIRE_ACTION", "repo.attachment_approval_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleMobilePairingBegin(req contract.Request) contract.Response {
	service := s.mobilePairingService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "MOBILE-0001", "ERROR", "RETRY", "mobile_pairing.unavailable", nil)
	}
	var payload contract.MobilePairingBeginPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	_, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok {
		return contract.ErrResponse(req.RequestID, "MOBILE-1001", "ERROR", "NONE", "mobile_pairing.server_not_activated", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := service.Begin(ctx, payload.ServerID)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "MOBILE-1002", "ERROR", "RETRY", "mobile_pairing.begin_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoCreateRequest(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoCreateRequestPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[payload.ServerID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "REPO-2001", "ERROR", "NONE", "repo.create_forbidden", nil)
	}
	result, err := service.BeginCreate(payload.ServerID, payload.DisplayName, payload.LocalPath)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleRepoAttachIntent(req contract.Request) contract.Response {
	service := s.repositoryLifecycleService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	var payload contract.RepoAttachIntentPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rs := s.repoByID(payload.RepoID)
	if rs == nil || rs.ServerID() != payload.ServerID {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	}
	snapshot := rs.Snapshot()
	if snapshot.Attached {
		return contract.ErrResponse(req.RequestID, "REPO-2003", "ERROR", "NONE", "repo.already_attached", nil)
	}
	result, err := service.BeginAttach(payload.ServerID, payload.RepoID, payload.LocalPath, snapshot.AttachmentPolicy == "required")
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleActivationBegin(req contract.Request) contract.Response {
	service := s.activationService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-0001", "ERROR", "RETRY", "activation.unavailable", nil)
	}
	var payload contract.ActivationBeginPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := service.Begin(ctx, payload)
	if err != nil {
		s.lg.Warnf("activation begin (server=%s address=%s): %v", payload.ServerID, payload.ServerAddress, err)
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1001", "ERROR", "RETRY", "activation.begin_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleActivationFinish(req contract.Request) contract.Response {
	defer clear(req.Payload)
	service := s.activationService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-0001", "ERROR", "RETRY", "activation.unavailable", nil)
	}
	var payload contract.ActivationFinishPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	defer clear(payload.OTP)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := service.Finish(ctx, payload)
	if err != nil {
		s.lg.Warnf("activation finish (server=%s address=%s): %v", payload.ServerID, payload.ServerAddress, err)
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1002", "ERROR", "RETRY", "activation.finish_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleActivationPending(req contract.Request) contract.Response {
	service := s.activationService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-0001", "ERROR", "RETRY", "activation.unavailable", nil)
	}
	var payload contract.ActivationPendingPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := service.Pending(ctx, payload)
	if err != nil {
		s.lg.Warnf("activation pending: %v", err)
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1003", "ERROR", "RETRY", "activation.pending_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleActivationResume(req contract.Request) contract.Response {
	service := s.activationService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-0001", "ERROR", "RETRY", "activation.unavailable", nil)
	}
	var payload contract.ActivationResumePayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := service.Resume(ctx, payload)
	if err != nil {
		s.lg.Warnf("activation resume (server=%s address=%s): %v", payload.ServerID, payload.ServerAddress, err)
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1004", "ERROR", "RETRY", "activation.resume_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

// handleHello implements system.hello — capability negotiation (§12).
func (s *Server) handleHello(req contract.Request) contract.Response {
	return contract.OKResponse(req.RequestID, contract.HelloResult{
		DaemonVersion:    "0.1.0",
		ProtocolVersions: []string{contract.Protocol},
		Capabilities:     s.capabilities(),
	})
}

// handleSystemStatus implements system.status.
func (s *Server) handleSystemStatus(req contract.Request) contract.Response {
	result := contract.SystemStatusResult{
		State:       "running",
		UptimeSec:   s.uptime(),
		Repos:       len(s.allRepos()),
		Activations: s.allActivations(),
	}
	if service := s.realmRemovalService(); service != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if recoveries, err := service.List(ctx); err == nil {
			result.Recoveries = recoveries
		}
	}
	if service := s.updateService(); service != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if status, err := service.Status(ctx); err == nil {
			result.Update = &status
		}
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleUpdateStatus(req contract.Request) contract.Response {
	service := s.updateService()
	if service == nil {
		return updateUnavailable(req.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := service.Status(ctx)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "UPDATE-1001", "ERROR", "RETRY_BACKOFF", "update.status_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleUpdatePlan(req contract.Request) contract.Response {
	service := s.updateService()
	if service == nil {
		return updateUnavailable(req.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := service.Plan(ctx)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "UPDATE-1002", "ERROR", "RETRY_BACKOFF", "update.plan_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleUpdateApply(req contract.Request) contract.Response {
	service := s.updateService()
	if service == nil {
		return updateUnavailable(req.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, err := service.Apply(ctx)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "UPDATE-1003", "ERROR", "REQUIRE_ACTION", "update.apply_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func updateUnavailable(requestID string) contract.Response {
	return contract.ErrResponse(requestID, "UPDATE-0001", "ERROR", "NONE", "update.unavailable", nil)
}

// handleRepoList implements repo.list.
func (s *Server) handleRepoList(req contract.Request) contract.Response {
	repos := s.allRepos()
	summaries := make([]contract.RepoSummary, len(repos))
	for i, rs := range repos {
		summaries[i] = rs.Summary()
	}
	return contract.OKResponse(req.RequestID, contract.RepoListResult{Repos: summaries})
}

// handleRepoStatus implements repo.status — full snapshot (§8).
func (s *Server) handleRepoStatus(req contract.Request) contract.Response {
	if req.RepoID == "" {
		return contract.ErrResponse(req.RequestID,
			"PROTO-0004", "ERROR", "NONE", "proto.missing_repo_id", nil)
	}
	rs := s.repoByID(req.RepoID)
	if rs == nil {
		return contract.ErrResponse(req.RequestID,
			"PROTO-0005", "ERROR", "NONE", "proto.repo_not_found",
			map[string]string{"repo_id": req.RepoID})
	}
	return contract.OKResponse(req.RequestID, rs.Snapshot())
}

// handleErrorList implements error.list — returns recent structured errors from
// the repo's errors.jsonl log. Accepts optional ErrorListPayload for filtering.
func (s *Server) handleErrorList(req contract.Request) contract.Response {
	var pl contract.ErrorListPayload
	_ = contract.DecodePayload(req.Payload, &pl)

	limit := pl.Limit
	if limit <= 0 {
		limit = 20
	}

	var repos []*RepoState
	if pl.RepoID != "" {
		rs := s.repoByID(pl.RepoID)
		if rs == nil {
			return contract.ErrResponse(req.RequestID,
				"PROTO-0005", "ERROR", "NONE", "proto.repo_not_found",
				map[string]string{"repo_id": pl.RepoID})
		}
		repos = []*RepoState{rs}
	} else {
		repos = s.allRepos()
	}

	var records []contract.ErrorRecord
	for _, rs := range repos {
		logPath := filepath.Join(rs.localPath, ".filees", "logs", "errors.jsonl")
		lines := readLastErrors(logPath, limit)
		for _, l := range lines {
			if r := parseErrLine(l, rs.id); r != nil {
				records = append(records, *r)
			}
		}
	}
	records = sortAndLimitErrors(records, limit)

	return contract.OKResponse(req.RequestID, contract.ErrorListResult{Errors: records})
}

// sortAndLimitErrors defines error.list ordering: oldest first across all
// repositories. Ties are deterministic so map iteration order cannot leak into
// the IPC response. Consumers that present newest-first may safely reverse it.
func sortAndLimitErrors(records []contract.ErrorRecord, limit int) []contract.ErrorRecord {
	sort.SliceStable(records, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, records[i].TS)
		right, rightErr := time.Parse(time.RFC3339Nano, records[j].TS)
		if leftErr == nil && rightErr == nil && !left.Equal(right) {
			return left.Before(right)
		}
		if records[i].TS != records[j].TS {
			return records[i].TS < records[j].TS
		}
		if records[i].RepoID != records[j].RepoID {
			return records[i].RepoID < records[j].RepoID
		}
		return records[i].ID < records[j].ID
	})
	if len(records) > limit {
		return records[len(records)-limit:]
	}
	return records
}

// handleRepoLockUnlock implements repo.lock and repo.unlock.
func (s *Server) handleRepoLockUnlock(req contract.Request, lock bool) contract.Response {
	if req.RepoID == "" {
		return protoErr(req.RequestID, "proto.missing_repo_id", nil)
	}
	rs := s.repoByID(req.RepoID)
	if rs == nil {
		return protoErr(req.RequestID, "proto.repo_not_found",
			map[string]string{"repo_id": req.RepoID})
	}
	if lock {
		s.mu.RLock()
		activation, activated := s.activations[rs.ServerID()]
		s.mu.RUnlock()
		if activated && activation.RealmAlias == "" {
			return contract.ErrResponse(req.RequestID, "REALM-2001", "ERROR", "REQUIRE_ACTION", "realm.alias_required", nil)
		}
	}
	var pl contract.RepoLockPayload
	if err := contract.DecodePayload(req.Payload, &pl); err != nil || len(pl.Paths) == 0 {
		return protoErr(req.RequestID, "proto.invalid_payload",
			map[string]string{"detail": "paths must be a non-empty array"})
	}

	// Validate that every path is absolute and inside the repo's working copy.
	sep := string(filepath.Separator)
	for _, p := range pl.Paths {
		if !filepath.IsAbs(p) {
			return contract.ErrResponse(req.RequestID,
				"LOCK-2002", "ERROR", "REQUIRE_ACTION", "lock.invalid_path",
				map[string]string{"path": p, "detail": "path must be absolute"})
		}
		clean := filepath.Clean(p)
		wc := filepath.Clean(rs.localPath)
		if clean != wc && !strings.HasPrefix(clean, wc+sep) {
			return contract.ErrResponse(req.RequestID,
				"LOCK-2002", "ERROR", "REQUIRE_ACTION", "lock.invalid_path",
				map[string]string{"path": p, "detail": "path is outside repository working copy"})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out string
	var err error
	if lock {
		out, err = rs.Lock(ctx, pl.Paths)
	} else {
		out, err = rs.Unlock(ctx, pl.Paths)
	}
	if err != nil {
		// A foreign hold is not a generic failure: it is somebody working on
		// the file right now, and the user can act on that only if told who
		// and until when. Both are known here, so they are sent as structured
		// details under their own message key rather than flattened into a
		// sentence the presentation layer would have to parse.
		var held *passport.HeldByOther
		if errors.As(err, &held) {
			details := map[string]string{"path": filepath.Base(held.Path)}
			if !held.Until.IsZero() {
				details["until"] = held.Until.UTC().Format(time.RFC3339)
			}
			if label := s.resolveHolderLabel(ctx, rs.ServerID(), held.Holder); label != "" {
				details["holder"] = label
			}
			return contract.ErrResponse(req.RequestID,
				"LOCK-2001", "ERROR", "REQUIRE_ACTION", "lock.held_by_other", details)
		}
		return contract.ErrResponse(req.RequestID,
			"LOCK-2001", "ERROR", "REQUIRE_ACTION", "lock.operation_failed",
			map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.LockResult{Output: out})
}

func (s *Server) handleRepoPublish(req contract.Request) contract.Response {
	if req.RepoID == "" {
		return protoErr(req.RequestID, "proto.missing_repo_id", nil)
	}
	rs := s.repoByID(req.RepoID)
	if rs == nil {
		return protoErr(req.RequestID, "proto.repo_not_found", map[string]string{"repo_id": req.RepoID})
	}
	var payload contract.RepoPublishPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	rev, err := rs.Publish(ctx, payload.Comment)
	if err != nil {
		if errors.Is(err, shout.ErrNothingToPublish) {
			return contract.ErrResponse(req.RequestID, "SHOUT-1001", "ERROR", "REQUIRE_ACTION", "shout.nothing_to_publish", nil)
		}
		if errors.Is(err, shout.ErrEmptyComment) || errors.Is(err, shout.ErrCommentHasControl) || errors.Is(err, shout.ErrCommentTooLong) {
			return contract.ErrResponse(req.RequestID, "SHOUT-1001", "ERROR", "REQUIRE_ACTION", "shout.invalid_comment", nil)
		}
		if strings.Contains(err.Error(), "REPO_READ_ONLY") {
			return contract.ErrResponse(req.RequestID, "SHOUT-1002", "ERROR", "NONE", "shout.read_only", nil)
		}
		return contract.ErrResponse(req.RequestID, "SHOUT-1003", "ERROR", "REQUIRE_ACTION", "shout.publish_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.RepoPublishResult{Revision: rev})
}

func (s *Server) handleNoticeList(req contract.Request) contract.Response {
	s.mu.RLock()
	repos := make([]*RepoState, 0, len(s.repos))
	for _, rs := range s.repos {
		repos = append(repos, rs)
	}
	s.mu.RUnlock()
	var notices []contract.Notice
	for _, rs := range repos {
		items, err := rs.Notices()
		if err != nil {
			return contract.ErrResponse(req.RequestID, "SHOUT-1004", "ERROR", "RETRY_LOCAL", "shout.list_failed", nil)
		}
		notices = append(notices, items...)
	}
	if notices == nil {
		notices = []contract.Notice{}
	}
	sort.SliceStable(notices, func(i, j int) bool {
		if notices[i].CreatedAt == notices[j].CreatedAt {
			return notices[i].ID < notices[j].ID
		}
		return notices[i].CreatedAt < notices[j].CreatedAt
	})
	return contract.OKResponse(req.RequestID, contract.NoticeListResult{Notices: notices})
}

func (s *Server) handleNoticeAck(req contract.Request) contract.Response {
	var payload contract.NoticeAckPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil || strings.TrimSpace(payload.NoticeID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	repos := make([]*RepoState, 0, len(s.repos))
	for _, rs := range s.repos {
		repos = append(repos, rs)
	}
	s.mu.RUnlock()
	for _, rs := range repos {
		if err := rs.AckNotice(payload.NoticeID); err != nil {
			return contract.ErrResponse(req.RequestID, "SHOUT-1005", "ERROR", "RETRY_LOCAL", "shout.ack_failed", nil)
		}
	}
	return contract.OKResponse(req.RequestID, map[string]bool{"acked": true})
}

// jsonErrLine is the on-disk format written by errmap.Sink.
type jsonErrLine struct {
	TS       string `json:"ts"`
	Scope    string `json:"scope"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Hint     string `json:"hint"`
	Msg      string `json:"msg"`
	Details  string `json:"details"`
}

func parseErrLine(raw, defaultRepoID string) *contract.ErrorRecord {
	var e jsonErrLine
	if json.Unmarshal([]byte(raw), &e) != nil {
		return nil
	}
	return &contract.ErrorRecord{
		ID:       e.TS + ":" + e.Code, // deterministic, good enough for v1
		TS:       e.TS,
		RepoID:   defaultRepoID,
		Code:     e.Code,
		Severity: e.Severity,
		Hint:     e.Hint,
		Msg:      e.Msg,
		Details:  e.Details,
	}
}

// resolveHolderLabel turns an opaque instance UID into something a person
// recognises. It fails soft on purpose: an unavailable control worker must
// degrade the message to "somebody else" rather than withhold the far more
// useful fact that the file is held at all.
func (s *Server) resolveHolderLabel(ctx context.Context, serverID, holder string) string {
	if holder == "" || serverID == "" {
		return ""
	}
	resolver := s.ownerLabelResolver()
	if resolver == nil {
		return ""
	}
	labels, err := resolver.Resolve(ctx, serverID, []string{holder})
	if err != nil {
		return ""
	}
	return labels[holder]
}
