package ipcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/historyexport"
)

// HistoryExportService runs Wehikuł czasu exports; *historyexport.Runner is
// the implementation. The handlers here own authorisation: every call re-checks
// that the activation still owns the repository the operation copies from.
type HistoryExportService interface {
	Begin(historyexport.Request) (historyexport.Record, error)
	Confirm(id string) (historyexport.Record, error)
	Cancel(id string) (historyexport.Record, error)
	Get(id string) (historyexport.Record, error)
}

// historyExportSelectionLimit bounds "Pobierz zaznaczone…"; a larger choice is
// a folder or the whole state.
const historyExportSelectionLimit = 10000

func (s *Server) SetHistoryExportService(service HistoryExportService) {
	s.mu.Lock()
	s.historyExports = service
	s.mu.Unlock()
}

func (s *Server) historyExportService() HistoryExportService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.historyExports
}

func historyOperation(rec historyexport.Record) contract.RepoHistoryOperation {
	out := contract.RepoHistoryOperation{
		OperationID: rec.ID, State: rec.State, ServerID: rec.ServerID, RepoID: rec.RepoID, Revision: rec.Revision,
		Subtree: rec.Subtree, DestinationParent: rec.Parent, FilesTotal: rec.FilesTotal, FilesDone: rec.FilesDone,
		BytesTotal: rec.BytesTotal, BytesDone: rec.BytesDone, SpaceAvailable: rec.SpaceAvailable,
		Renamed: []contract.RepoHistoryRename{}, Skipped: []contract.RepoHistorySkip{},
		WhaleExcluded: rec.WhaleExcluded, WhaleFiles: rec.WhaleFiles, WhaleBytes: rec.WhaleBytes,
		Diagnostic: rec.Error, CleanupError: rec.CleanupError,
	}
	if rec.State == historyexport.StateComplete {
		out.FinalPath = rec.Final
	}
	if rec.CleanupError != "" || (rec.State == historyexport.StateFailed && rec.Final != "") {
		out.StagingPath = rec.Stage
		out.FinalPath = rec.Final
	}
	for _, file := range rec.Files {
		out.SelectedPaths = append(out.SelectedPaths, file.Path)
	}
	for _, rename := range rec.Renamed {
		out.Renamed = append(out.Renamed, contract.RepoHistoryRename{RepoPath: rename.RepoPath, LocalPath: rename.LocalPath})
	}
	for _, skip := range rec.Skipped {
		out.Skipped = append(out.Skipped, contract.RepoHistorySkip{RepoPath: skip.RepoPath, Reason: skip.Reason, Files: skip.Files, Bytes: skip.Bytes})
	}
	return out
}

func (s *Server) historyExportRefusal(req contract.Request, err error) contract.Response {
	switch {
	case errors.Is(err, historyexport.ErrDestination):
		s.lg.Warnf("history export destination refused: %v", err)
		return contract.ErrResponse(req.RequestID, "HISTORY-2005", "ERROR", "REQUIRE_ACTION", "history.destination_refused", nil)
	case errors.Is(err, historyexport.ErrInsufficientSpace):
		return contract.ErrResponse(req.RequestID, "HISTORY-2006", "ERROR", "REQUIRE_ACTION", "history.insufficient_space", nil)
	case errors.Is(err, historyexport.ErrUnknownOperation):
		return contract.ErrResponse(req.RequestID, "HISTORY-2007", "ERROR", "NONE", "history.operation_unknown", nil)
	case errors.Is(err, historyexport.ErrState):
		return contract.ErrResponse(req.RequestID, "HISTORY-2008", "ERROR", "NONE", "history.operation_state", nil)
	case errors.Is(err, historyexport.ErrInvalidRequest):
		return historyInvalid(req)
	}
	return s.historyReadFailed(req, err)
}

func newHistoryOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Server) handleHistoryFetch(req contract.Request) contract.Response {
	exports := s.historyExportService()
	if exports == nil {
		return historyUnavailable(req)
	}
	var p contract.RepoHistoryFetchPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.SnapshotID == "" || !filepath.IsAbs(p.DestinationParent) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	snap, svc, refusal, ok := s.historyContext(req, p.SnapshotID)
	if !ok {
		return refusal
	}
	if snap.Revision < 1 || p.UTCOffsetMinutes < -14*60 || p.UTCOffsetMinutes > 14*60 {
		return historyInvalid(req)
	}
	moment, err := time.Parse(time.RFC3339Nano, snap.RequestedMoment)
	if err != nil {
		return historyInvalid(req)
	}
	repo := s.RepoState(snap.ServerID, snap.RepoID)
	if repo == nil {
		return historyForbidden(req)
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	// Re-authorise against the repository itself: the same URL answering with
	// another UUID is another repository, and its rN is not the snapshot's.
	uuid, err := svc.HistoryRepositoryUUID(ctx, snap.ServerID, snap.url)
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	if uuid != snap.RepositoryUUID {
		s.historySnaps.forget(snap.SnapshotID)
		return historySnapshotUnknown(req)
	}
	id, err := newHistoryOperationID()
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	request := historyexport.Request{
		ID: id, ServerID: snap.ServerID, RepoID: snap.RepoID, RepoName: repo.Summary().DisplayName,
		RepoURL: snap.url, RepositoryUUID: uuid, Revision: snap.Revision,
		Moment: moment.In(time.FixedZone("", p.UTCOffsetMinutes*60)), Parent: p.DestinationParent,
	}
	switch p.Selection {
	case contract.HistorySelectionAll:
	case contract.HistorySelectionSubtree:
		path, valid := cleanHistoryPath(p.Path)
		if !valid || path == "" {
			return historyInvalid(req)
		}
		request.Subtree = path
	case contract.HistorySelectionPaths:
		files, refusal, ok := s.historySelectedFiles(ctx, req, snap, svc, p.Paths)
		if !ok {
			return refusal
		}
		request.Files = files
	default:
		return historyInvalid(req)
	}
	rec, err := exports.Begin(request)
	if err != nil {
		return s.historyExportRefusal(req, err)
	}
	return contract.OKResponse(req.RequestID, historyOperation(rec))
}

// historySelectedFiles takes each file's size from its folder at the snapshot
// revision, one listing per folder, so the confirmation shows a real total.
func (s *Server) historySelectedFiles(ctx context.Context, req contract.Request, snap historySnapshot, svc HistoryService, paths []string) ([]historyexport.Node, contract.Response, bool) {
	if len(paths) == 0 || len(paths) > historyExportSelectionLimit {
		return nil, historyInvalid(req), false
	}
	byFolder := make(map[string][]string)
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		path, valid := cleanHistoryPath(raw)
		if !valid || path == "" || seen[path] {
			return nil, historyInvalid(req), false
		}
		seen[path] = true
		folder, name := "", path
		if i := lastSlash(path); i >= 0 {
			folder, name = path[:i], path[i+1:]
		}
		byFolder[folder] = append(byFolder[folder], name)
	}
	var files []historyexport.Node
	for folder, names := range byFolder {
		entries, err := svc.HistoryList(ctx, snap.ServerID, snap.url, folder, snap.Revision)
		if errors.Is(err, ErrHistoryPathAbsent) {
			return nil, contract.ErrResponse(req.RequestID, "HISTORY-2004", "ERROR", "NONE", "history.path_absent", nil), false
		}
		if err != nil {
			return nil, s.historyReadFailed(req, err), false
		}
		listed := make(map[string]contract.RepoHistoryEntry, len(entries))
		for _, entry := range entries {
			listed[entry.Name] = entry
		}
		for _, name := range names {
			entry, found := listed[name]
			if !found {
				return nil, contract.ErrResponse(req.RequestID, "HISTORY-2004", "ERROR", "NONE", "history.path_absent", nil), false
			}
			if entry.Kind != "file" || entry.Size == nil {
				return nil, historyInvalid(req), false
			}
			path := name
			if folder != "" {
				path = folder + "/" + name
			}
			files = append(files, historyexport.Node{Path: path, Kind: "file", Size: *entry.Size})
		}
	}
	return files, contract.Response{}, true
}

func lastSlash(path string) int {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return i
		}
	}
	return -1
}

// handleHistoryOperation serves status, confirmation and cancellation. An
// operation whose repository the activation no longer owns is refused, even
// just for reading its status.
func (s *Server) handleHistoryOperation(req contract.Request) contract.Response {
	exports := s.historyExportService()
	if exports == nil {
		return historyUnavailable(req)
	}
	var p contract.RepoHistoryOperationPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.OperationID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	rec, err := exports.Get(p.OperationID)
	if err != nil {
		return s.historyExportRefusal(req, err)
	}
	if _, allowed := s.historyRepo(rec.ServerID, rec.RepoID); !allowed {
		return historyForbidden(req)
	}
	switch req.Command {
	case contract.CmdRepoHistoryConfirm:
		rec, err = exports.Confirm(p.OperationID)
	case contract.CmdRepoHistoryCancel:
		rec, err = exports.Cancel(p.OperationID)
	}
	if err != nil {
		return s.historyExportRefusal(req, err)
	}
	return contract.OKResponse(req.RequestID, historyOperation(rec))
}
