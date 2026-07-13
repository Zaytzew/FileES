package ipcserver

import (
	"encoding/json"
	"path/filepath"

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
	case contract.CmdRepoList:
		return s.handleRepoList(req)
	case contract.CmdRepoStatus:
		return s.handleRepoStatus(req)
	case contract.CmdErrorList:
		return s.handleErrorList(req)
	default:
		return contract.ErrResponse(req.RequestID,
			"PROTO-0003", "ERROR", "NONE", "proto.unknown_command",
			map[string]string{"command": req.Command})
	}
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
		State:     "running",
		UptimeSec: s.uptime(),
		Repos:     len(s.allRepos()),
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
	if len(records) > limit {
		records = records[len(records)-limit:]
	}

	return contract.OKResponse(req.RequestID, contract.ErrorListResult{Errors: records})
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
