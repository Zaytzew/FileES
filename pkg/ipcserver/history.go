package ipcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	contract "filees/pkg/contract/v1"
)

// HistoryService reads one repository as it was, for Wehikuł czasu
// (concepts/REPOSITORY_HISTORY_CONCEPT.md §6). The handlers own the policy -
// who may ask, which repository a snapshot names, paging and budgets. The
// service only talks to the repository.
type HistoryService interface {
	// HistoryEnabled is false when this client cannot read history at all
	// (no native helper); the capability is not advertised then.
	HistoryEnabled() bool
	HistoryRepositoryUUID(ctx context.Context, serverID, repoURL string) (string, error)
	// HistoryRevisionAt is the newest revision not later than moment, with its
	// date; 0 and "" when nothing was committed that early.
	HistoryRevisionAt(ctx context.Context, serverID, repoURL string, moment time.Time) (int64, string, error)
	// HistoryLog returns newest..oldest, newest first, with changed paths.
	HistoryLog(ctx context.Context, serverID, repoURL string, newest, oldest int64, limit int) ([]HistoryLogEntry, error)
	// HistoryList lists one repository-relative folder ("" is the root) at a
	// revision, sorted by name. ErrHistoryPathAbsent when it was not a folder.
	HistoryList(ctx context.Context, serverID, repoURL, path string, revision int64) ([]contract.RepoHistoryEntry, error)
}

type HistoryLogEntry struct {
	Revision int64
	Date     string
	Author   string
	Shout    string
	Changes  []contract.RepoHistoryChange
}

var ErrHistoryPathAbsent = errors.New("history: path was not a folder at that revision")

const (
	historyCommitsPage    = 50
	historyChangesPage    = 500
	historyListPage       = 500
	historySnapshotBudget = 64
	historyReadTimeout    = 2 * time.Minute
)

func (s *Server) SetHistoryService(service HistoryService) {
	s.mu.Lock()
	s.historyReads = service
	s.mu.Unlock()
}

func (s *Server) historyService() HistoryService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.historyReads
}

type historySnapshot struct {
	contract.RepoHistorySnapshot
	url string
}

// historySnapshotStore is bounded and in memory. A GUI that reconnects after
// the daemon restarted resolves again; nothing here is worth persisting.
type historySnapshotStore struct {
	mu    sync.Mutex
	byID  map[string]historySnapshot
	order []string
}

func (h *historySnapshotStore) remember(snap historySnapshot) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	snap.SnapshotID = id
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byID == nil {
		h.byID = make(map[string]historySnapshot)
	}
	for len(h.order) >= historySnapshotBudget {
		delete(h.byID, h.order[0])
		h.order = h.order[1:]
	}
	h.byID[id] = snap
	h.order = append(h.order, id)
	return id, nil
}

func (h *historySnapshotStore) lookup(id string) (historySnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snap, ok := h.byID[id]
	return snap, ok
}

func (h *historySnapshotStore) forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byID, id)
	for i, known := range h.order {
		if known == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

func historyUnavailable(req contract.Request) contract.Response {
	return contract.ErrResponse(req.RequestID, "HISTORY-0001", "ERROR", "RETRY", "history.unavailable", nil)
}

func historyForbidden(req contract.Request) contract.Response {
	return contract.ErrResponse(req.RequestID, "HISTORY-2001", "ERROR", "NONE", "history.forbidden", nil)
}

func historySnapshotUnknown(req contract.Request) contract.Response {
	return contract.ErrResponse(req.RequestID, "HISTORY-2002", "ERROR", "REQUIRE_ACTION", "history.snapshot_unknown", nil)
}

func historyInvalid(req contract.Request) contract.Response {
	return contract.ErrResponse(req.RequestID, "HISTORY-2003", "ERROR", "REQUIRE_ACTION", "history.invalid_request", nil)
}

func (s *Server) historyReadFailed(req contract.Request, err error) contract.Response {
	s.lg.Warnf("repository history read failed: %v", err)
	return contract.ErrResponse(req.RequestID, "HISTORY-1001", "ERROR", "RETRY", "history.read_failed", nil)
}

// historyRepo is the owner gate. Until the server filters history by grant
// epoch (§12), only the realm that owns a repository reads its past; a guest
// with valid credentials gets a refusal, not a partial view.
func (s *Server) historyRepo(serverID, repoID string) (string, bool) {
	s.mu.RLock()
	activation, exists := s.activations[serverID]
	s.mu.RUnlock()
	if !exists || activation.RealmID == "" {
		return "", false
	}
	repo := s.RepoState(serverID, repoID)
	if repo == nil {
		return "", false
	}
	summary := repo.Summary()
	if summary.OwnerRealmID != activation.RealmID || summary.URL == "" || summary.Purpose != "" {
		return "", false
	}
	return summary.URL, true
}

// historyContext re-authorises a snapshot. A repository relocated or
// recreated under another URL invalidates it rather than reading the same rN
// from a different repository.
func (s *Server) historyContext(req contract.Request, snapshotID string) (historySnapshot, HistoryService, contract.Response, bool) {
	svc := s.historyService()
	if svc == nil || !svc.HistoryEnabled() {
		return historySnapshot{}, nil, historyUnavailable(req), false
	}
	snap, found := s.historySnaps.lookup(snapshotID)
	if !found {
		return historySnapshot{}, nil, historySnapshotUnknown(req), false
	}
	url, allowed := s.historyRepo(snap.ServerID, snap.RepoID)
	if !allowed {
		return historySnapshot{}, nil, historyForbidden(req), false
	}
	if url != snap.url {
		s.historySnaps.forget(snapshotID)
		return historySnapshot{}, nil, historySnapshotUnknown(req), false
	}
	return snap, svc, contract.Response{}, true
}

// historyMoment reads RFC 3339 to the microsecond, the repository's precision.
func historyMoment(raw string) (time.Time, bool) {
	moment, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return moment.UTC().Truncate(time.Microsecond), true
}

func (s *Server) handleHistoryResolve(req contract.Request) contract.Response {
	svc := s.historyService()
	if svc == nil || !svc.HistoryEnabled() {
		return historyUnavailable(req)
	}
	var p contract.RepoHistoryResolvePayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	moment := time.Now().UTC().Truncate(time.Microsecond)
	if p.MomentUTC != "" {
		var ok bool
		if moment, ok = historyMoment(p.MomentUTC); !ok {
			return historyInvalid(req)
		}
	}
	target := moment
	switch p.Boundary {
	case "", contract.HistoryBoundaryAt:
	case contract.HistoryBoundaryBefore:
		target = moment.Add(-time.Microsecond)
	default:
		return historyInvalid(req)
	}
	url, allowed := s.historyRepo(p.ServerID, p.RepoID)
	if !allowed {
		return historyForbidden(req)
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	uuid, err := svc.HistoryRepositoryUUID(ctx, p.ServerID, url)
	if err == nil && uuid == "" {
		err = errors.New("repository UUID is empty")
	}
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	revision, date, err := svc.HistoryRevisionAt(ctx, p.ServerID, url, target)
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	snap := historySnapshot{url: url, RepoHistorySnapshot: contract.RepoHistorySnapshot{
		ServerID: p.ServerID, RepoID: p.RepoID, RepositoryUUID: uuid, Revision: revision,
		RequestedMoment: moment.Format(time.RFC3339Nano), SavedAt: date,
	}}
	if snap.SnapshotID, err = s.historySnaps.remember(snap); err != nil {
		return s.historyReadFailed(req, err)
	}
	return contract.OKResponse(req.RequestID, snap.RepoHistorySnapshot)
}

// A commits cursor carries the resolved range, so later pages do not resolve
// the interval's dates again.
func historyCommitsCursor(newest, oldest int64) string {
	return fmt.Sprintf("r%d:%d", newest, oldest)
}

func parseHistoryCommitsCursor(raw string) (int64, int64, bool) {
	body, ok := strings.CutPrefix(raw, "r")
	if !ok {
		return 0, 0, false
	}
	left, right, ok := strings.Cut(body, ":")
	if !ok {
		return 0, 0, false
	}
	newest, errNewest := strconv.ParseInt(left, 10, 64)
	oldest, errOldest := strconv.ParseInt(right, 10, 64)
	if errNewest != nil || errOldest != nil || oldest < 1 || newest < oldest {
		return 0, 0, false
	}
	return newest, oldest, true
}

func (s *Server) handleHistoryCommits(req contract.Request) contract.Response {
	var p contract.RepoHistoryCommitsPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.SnapshotID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	snap, svc, refusal, ok := s.historyContext(req, p.SnapshotID)
	if !ok {
		return refusal
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	var newest, oldest int64
	if p.Cursor != "" {
		if newest, oldest, ok = parseHistoryCommitsCursor(p.Cursor); !ok {
			return historyInvalid(req)
		}
	} else {
		from, okFrom := historyMoment(p.From)
		to, okTo := historyMoment(p.To)
		// No silent clipping: a reversed interval is the caller's mistake.
		if !okFrom || !okTo || from.After(to) {
			return historyInvalid(req)
		}
		var err error
		if newest, _, err = svc.HistoryRevisionAt(ctx, snap.ServerID, snap.url, to); err != nil {
			return s.historyReadFailed(req, err)
		}
		before, _, err := svc.HistoryRevisionAt(ctx, snap.ServerID, snap.url, from.Add(-time.Microsecond))
		if err != nil {
			return s.historyReadFailed(req, err)
		}
		oldest = before + 1
	}
	result := contract.RepoHistoryCommitsResult{Commits: []contract.RepoHistoryCommit{}}
	if newest < oldest {
		return contract.OKResponse(req.RequestID, result)
	}
	entries, err := svc.HistoryLog(ctx, snap.ServerID, snap.url, newest, oldest, historyCommitsPage+1)
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	more := len(entries) > historyCommitsPage
	if more {
		entries = entries[:historyCommitsPage]
	}
	for _, entry := range entries {
		if entry.Revision < oldest || entry.Revision > newest {
			return s.historyReadFailed(req, fmt.Errorf("log entry r%d outside r%d:%d", entry.Revision, newest, oldest))
		}
		result.Commits = append(result.Commits, contract.RepoHistoryCommit{
			Revision: entry.Revision, Date: entry.Date, Author: entry.Author, Shout: entry.Shout, ChangedCount: len(entry.Changes),
		})
	}
	if more {
		if next := entries[len(entries)-1].Revision - 1; next >= oldest {
			result.NextCursor = historyCommitsCursor(next, oldest)
		}
	}
	return contract.OKResponse(req.RequestID, result)
}

func parseHistoryOffset(raw string, total int) (int, bool) {
	if raw == "" {
		return 0, true
	}
	body, ok := strings.CutPrefix(raw, "o")
	if !ok {
		return 0, false
	}
	offset, err := strconv.Atoi(body)
	if err != nil || offset < 1 || offset >= total {
		return 0, false
	}
	return offset, true
}

func historyPage(offset, page, total int) (int, string) {
	end := min(offset+page, total)
	if end < total {
		return end, "o" + strconv.Itoa(end)
	}
	return end, ""
}

func (s *Server) handleHistoryChanges(req contract.Request) contract.Response {
	var p contract.RepoHistoryChangesPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.SnapshotID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	snap, svc, refusal, ok := s.historyContext(req, p.SnapshotID)
	if !ok {
		return refusal
	}
	if p.Revision < 1 {
		return historyInvalid(req)
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	entries, err := svc.HistoryLog(ctx, snap.ServerID, snap.url, p.Revision, p.Revision, 1)
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	if len(entries) != 1 || entries[0].Revision != p.Revision {
		return historyInvalid(req)
	}
	changed := append([]contract.RepoHistoryChange(nil), entries[0].Changes...)
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	offset, ok := parseHistoryOffset(p.Cursor, len(changed))
	if !ok {
		return historyInvalid(req)
	}
	end, next := historyPage(offset, historyChangesPage, len(changed))
	return contract.OKResponse(req.RequestID, contract.RepoHistoryChangesResult{
		Revision: p.Revision, Changed: append([]contract.RepoHistoryChange{}, changed[offset:end]...), NextCursor: next,
	})
}

// cleanHistoryPath accepts a repository-relative folder as the GUI got it from
// an earlier listing. The service escapes it into a URL; nothing here may
// climb out of the repository.
func cleanHistoryPath(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\") {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return raw, true
}

func (s *Server) handleHistoryList(req contract.Request) contract.Response {
	var p contract.RepoHistoryListPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.SnapshotID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	snap, svc, refusal, ok := s.historyContext(req, p.SnapshotID)
	if !ok {
		return refusal
	}
	path, ok := cleanHistoryPath(p.Path)
	if !ok {
		return historyInvalid(req)
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	entries, err := svc.HistoryList(ctx, snap.ServerID, snap.url, path, snap.Revision)
	if errors.Is(err, ErrHistoryPathAbsent) {
		return contract.ErrResponse(req.RequestID, "HISTORY-2004", "ERROR", "NONE", "history.path_absent", nil)
	}
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	offset, ok := parseHistoryOffset(p.Cursor, len(entries))
	if !ok {
		return historyInvalid(req)
	}
	end, next := historyPage(offset, historyListPage, len(entries))
	return contract.OKResponse(req.RequestID, contract.RepoHistoryListResult{
		Revision: snap.Revision, Path: path, Entries: append([]contract.RepoHistoryEntry{}, entries[offset:end]...), NextCursor: next,
	})
}
