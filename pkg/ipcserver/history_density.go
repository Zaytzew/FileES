package ipcserver

import (
	"context"
	"strconv"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/historyindex"
)

// HistoryDensityService answers the chart from the repository's index and
// keeps the index growing while a window asks. The handler owns the owner
// gate; the service owns indexing and its budgets.
type HistoryDensityService interface {
	HistoryDensity(ctx context.Context, serverID, repoURL, uuid string, q historyindex.Query) (HistoryDensity, error)
}

type HistoryDensity struct {
	historyindex.Result
	Head       int64
	Indexing   bool
	Diagnostic string
}

const historyDensityPage = 5000

func (s *Server) SetHistoryDensityService(service HistoryDensityService) {
	s.mu.Lock()
	s.historyDensity = service
	s.mu.Unlock()
}

func (s *Server) historyDensityService() HistoryDensityService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.historyDensity
}

func historyUnix(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

func (s *Server) handleHistoryDensity(req contract.Request) contract.Response {
	density := s.historyDensityService()
	if density == nil {
		return historyUnavailable(req)
	}
	var p contract.RepoHistoryDensityPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.SnapshotID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	snap, _, refusal, ok := s.historyContext(req, p.SnapshotID)
	if !ok {
		return refusal
	}
	switch p.BucketHours {
	case 1, 2, 4, 6, 12, 24:
	default:
		return historyInvalid(req)
	}
	if p.UTCOffsetMinutes < -12*60 || p.UTCOffsetMinutes > 14*60 {
		return historyInvalid(req)
	}
	q := historyindex.Query{BucketSeconds: int64(p.BucketHours) * 3600, OffsetSeconds: int64(p.UTCOffsetMinutes) * 60, Limit: historyDensityPage}
	if p.From != "" {
		if q.From, ok = historyMoment(p.From); !ok {
			return historyInvalid(req)
		}
	}
	if p.To != "" {
		if q.To, ok = historyMoment(p.To); !ok {
			return historyInvalid(req)
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return historyInvalid(req)
	}
	if p.Cursor != "" {
		body, found := strings.CutPrefix(p.Cursor, "b")
		after, err := strconv.ParseInt(body, 10, 64)
		if !found || err != nil || after == 0 {
			return historyInvalid(req)
		}
		q.After = after
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
	defer cancel()
	d, err := density.HistoryDensity(ctx, snap.ServerID, snap.url, snap.RepositoryUUID, q)
	if err != nil {
		return s.historyReadFailed(req, err)
	}
	result := contract.RepoHistoryDensityResult{
		HeadRevision: max(d.Head, d.Indexed), IndexedRevision: d.Indexed, Indexing: d.Indexing,
		Buckets: make([]contract.RepoHistoryDensityBucket, 0, len(d.Buckets)), IndexDiagnostic: d.Diagnostic,
	}
	if d.FirstUnix != 0 {
		result.FirstDate, result.LastIndexedDate = historyUnix(d.FirstUnix), historyUnix(d.LastUnix)
	}
	for _, b := range d.Buckets {
		result.Buckets = append(result.Buckets, contract.RepoHistoryDensityBucket{
			Start: historyUnix(b.Start), End: historyUnix(b.End), ChangedPaths: b.ChangedPaths,
			UniquePaths: b.UniquePaths, UniqueExact: b.UniqueExact, Commits: b.Commits, Shouts: b.Shouts,
		})
	}
	if d.More && len(d.Buckets) > 0 {
		result.NextCursor = "b" + strconv.FormatInt(d.Buckets[len(d.Buckets)-1].Start, 10)
	}
	return contract.OKResponse(req.RequestID, result)
}
