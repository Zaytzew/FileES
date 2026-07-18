package ipcserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
)

// dispatch routes a validated request to the appropriate handler.
// Unknown commands return a structured error; the connection is not closed.
func (s *Server) dispatch(req contract.Request) contract.Response {
	switch req.Command {
	case contract.CmdSystemHello:
		return s.handleHello(req)
	case contract.CmdSystemStatus:
		return s.handleSystemStatus(req)
	case contract.CmdActivationBegin:
		return s.handleActivationBegin(req)
	case contract.CmdActivationFinish:
		return s.handleActivationFinish(req)
	case contract.CmdRepoList:
		return s.handleRepoList(req)
	case contract.CmdRepoStatus:
		return s.handleRepoStatus(req)
	case contract.CmdErrorList:
		return s.handleErrorList(req)
	case contract.CmdRepoLock:
		return s.handleRepoLockUnlock(req, true)
	case contract.CmdRepoUnlock:
		return s.handleRepoLockUnlock(req, false)
	default:
		return contract.ErrResponse(req.RequestID,
			"PROTO-0003", "ERROR", "NONE", "proto.unknown_command",
			map[string]string{"command": req.Command})
	}
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
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1001", "ERROR", "RETRY", "activation.begin_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

func (s *Server) handleActivationFinish(req contract.Request) contract.Response {
	service := s.activationService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-0001", "ERROR", "RETRY", "activation.unavailable", nil)
	}
	var payload contract.ActivationFinishPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := service.Finish(ctx, payload)
	payload.OTP = ""
	if err != nil {
		return contract.ErrResponse(req.RequestID, "ACTIVATION-1002", "ERROR", "RETRY", "activation.finish_failed", nil)
	}
	return contract.OKResponse(req.RequestID, result)
}

// handleHello implements system.hello — capability negotiation (§12).
func (s *Server) handleHello(req contract.Request) contract.Response {
	return contract.OKResponse(req.RequestID, contract.HelloResult{
		DaemonVersion:    "0.1.0",
		ProtocolVersions: []string{contract.Protocol},
		Capabilities:     contract.AllCapabilities,
	})
}

// handleSystemStatus implements system.status.
func (s *Server) handleSystemStatus(req contract.Request) contract.Response {
	return contract.OKResponse(req.RequestID, contract.SystemStatusResult{
		State:       "running",
		UptimeSec:   s.uptime(),
		Repos:       len(s.allRepos()),
		Activations: s.allActivations(),
	})
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
		return contract.ErrResponse(req.RequestID,
			"LOCK-2001", "ERROR", "REQUIRE_ACTION", "lock.operation_failed",
			map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.LockResult{Output: out})
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
