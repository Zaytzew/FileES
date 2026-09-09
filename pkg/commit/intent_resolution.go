package commit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

const intentCacheSchema = "filees.commit-cache/v2"

// One actionable diagnostic for an unchanged HOLD, not a new error each tick.
// Reuse the already-journaled wrapper to prevent the publication caller logging
// the same fault again. This never chooses a fallback or changes the queue.
func (s *Service) reportIntentHold(now time.Time) error {
	s.mu.Lock()
	var paths []string
	for _, e := range s.staging {
		if e.Op == watcher.RenameUncertain {
			paths = append(paths, e.Rel)
		}
	}
	s.mu.Unlock()
	sort.Strings(paths)
	key := strings.Join(paths, "\x00")
	detail := fmt.Sprintf("ambiguous file intents: %d paths; publication held; explicit resolution required; no delete/add fallback", len(paths))
	fault := errcat.New("intent.ambiguous", map[string]string{"detail": detail}, nil)
	if key != s.intentDiagnosticKey || now.Sub(s.intentDiagnosticAt) >= 15*time.Minute {
		s.ErrSink.Emit(errmap.Classify(fault))
		s.Logger.Warnf("%s", detail)
		s.intentDiagnosticKey, s.intentDiagnosticAt = key, now
	}
	return &recoveryFailure{cause: fault, detail: detail}
}

// The receipt and the converted queue share one atomic replacement. There is
// no crash window with approval stored but its interpretation not stored (or
// vice versa). Keep the last 64 receipts for retry after lost IPC responses.
type intentReceipt struct {
	PlanID      string                `json:"plan_id"`
	RepoID      string                `json:"repo_id"`
	AcceptedAt  string                `json:"accepted_at"`
	Fingerprint string                `json:"fingerprint"`
	Paths       []contract.IntentPath `json:"paths"`
}

type intentCache struct {
	Schema   string          `json:"schema"`
	Entries  []cacheEntry    `json:"entries"`
	Receipts []intentReceipt `json:"intent_receipts"`
}

type intentPlanState struct {
	plan        contract.IntentPlan
	fingerprint string
	expires     time.Time
}

func decodeIntentCache(data []byte, entries *[]cacheEntry, receipts *[]intentReceipt) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var value intentCache
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.Schema != intentCacheSchema {
			return errors.New("unsupported commit cache schema")
		}
		*entries, *receipts = value.Entries, value.Receipts
		return nil
	}
	return json.Unmarshal(data, entries)
}

func intentCacheValue(entries []cacheEntry, receipts []intentReceipt) any {
	if len(receipts) == 0 {
		return entries
	}
	return intentCache{Schema: intentCacheSchema, Entries: entries, Receipts: append([]intentReceipt(nil), receipts...)}
}

// PlanIntents reads only. The bounded plan is tied to this running repository
// service, the whole queue, SVN status, and SHA256 of each candidate addition.
// Nothing supplied by the caller is used as a filesystem path.
func (s *Service) PlanIntents(ctx context.Context) (*contract.IntentPlan, error) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	paths, fingerprint, err := s.inspectIntents(ctx)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(10 * time.Minute)
	plan := contract.IntentPlan{PlanID: uuid.NewString(), RepoID: s.repoID, Choice: contract.IntentDeleteAdd, ExpiresAt: expires.UTC().Format(time.RFC3339), Paths: paths}
	s.intentPlan = &intentPlanState{plan: plan, fingerprint: fingerprint, expires: expires}
	copy := plan
	copy.Paths = append([]contract.IntentPath(nil), paths...)
	return &copy, nil
}

// ApplyIntents accepts a closed choice, not a user-edited plan. It changes only
// unscheduled uncertainty into additions. The ordinary commit pipeline retains
// responsibility for access, locks, scheduling, transaction and publication.
func (s *Service) ApplyIntents(ctx context.Context, planID, choice string) (*contract.IntentApplyResult, error) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if choice != contract.IntentDeleteAdd || planID == "" {
		return nil, errors.New("unsupported intent decision")
	}
	s.mu.Lock()
	for _, receipt := range s.intentReceipts {
		if receipt.PlanID == planID && receipt.RepoID == s.repoID {
			s.mu.Unlock()
			return &contract.IntentApplyResult{PlanID: planID, State: "queued"}, nil
		}
	}
	s.mu.Unlock()
	plan := s.intentPlan
	if plan == nil || plan.plan.PlanID != planID || time.Now().After(plan.expires) {
		return nil, errors.New("intent plan expired or unavailable; request a new plan")
	}
	paths, fingerprint, err := s.inspectIntents(ctx)
	if err != nil {
		return nil, err
	}
	if fingerprint != plan.fingerprint {
		return nil, errors.New("files or pending operations changed; request a new plan")
	}
	if time.Now().After(plan.expires) {
		return nil, errors.New("intent plan expired during inspection; request a new plan")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.cacheSaveMu.Lock()
	defer s.cacheSaveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.intentEntriesLocked()
	for i := range entries {
		if entries[i].Op == "rename_uncertain" {
			entries[i].Op = "added"
			entries[i].ResolutionID = planID
		}
	}
	receipts := append(append([]intentReceipt(nil), s.intentReceipts...), intentReceipt{
		PlanID: planID, RepoID: s.repoID, AcceptedAt: time.Now().UTC().Format(time.RFC3339Nano), Fingerprint: fingerprint, Paths: paths,
	})
	if len(receipts) > 64 {
		receipts = receipts[len(receipts)-64:]
	}
	// Never create an absent cache directory / moved working copy here.
	if !s.workingCopyAvailable(s.wc) {
		return nil, errors.New("working copy unavailable")
	}
	if err := atomicWriteJSONSliceInExistingDir(s.cachePath, intentCacheValue(entries, receipts)); err != nil {
		return nil, err
	}
	for _, item := range s.staging {
		if item.Op == watcher.RenameUncertain {
			item.Op = watcher.Added
			item.ResolutionID = planID
			item.ver++
		}
	}
	s.intentReceipts = receipts
	s.intentPlan = nil
	return &contract.IntentApplyResult{PlanID: planID, State: "queued"}, nil
}

func (s *Service) intentEntriesLocked() []cacheEntry {
	entries := make([]cacheEntry, 0, len(s.staging))
	for _, it := range s.staging {
		entries = append(entries, cacheEntry{ResolutionID: it.ResolutionID, Rel: it.Rel, Abs: it.Abs, OldRel: it.OldRel, RenameVerified: it.RenameVerified, MoveScheduled: it.MoveScheduled, IsDir: it.IsDir, Op: opName(it.Op), FirstSeen: it.FirstSeen, LastSeen: it.LastSeen})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Rel < entries[j].Rel })
	return entries
}

// Caller holds wcOpMu. It prevents the service's watcher merge, update and
// publication from interleaving; external edits are checked afresh, not locked.
func (s *Service) inspectIntents(ctx context.Context) ([]contract.IntentPath, string, error) {
	if s.Cli == nil || s.wc == "" || s.repoID == "" || s.cachePath == "" || !s.workingCopyAvailable(s.wc) {
		return nil, "", errors.New("intent resolution unavailable")
	}
	transaction, transactionErr := s.readIntent(s.wc)
	if transactionErr != nil || (transaction != nil && transaction.Phase != "done") {
		return nil, "", errors.New("unresolved commit transaction; resolve recovery first")
	}
	s.mu.Lock()
	entries := s.intentEntriesLocked()
	s.mu.Unlock()
	var selected []cacheEntry
	uncertain := 0
	for _, e := range entries {
		if e.MoveScheduled || e.RenameVerified || e.OldRel != "" || e.Op == "renamed" {
			return nil, "", errors.New("scheduled or verified move cannot be reinterpreted")
		}
		if e.Op != "rename_uncertain" && e.Op != "deleted" {
			continue
		}
		if e.IsDir {
			return nil, "", errors.New("directory intent requires separate resolution")
		}
		if e.Op == "rename_uncertain" {
			uncertain++
		}
		selected = append(selected, e)
	}
	if uncertain == 0 || len(selected) > 128 {
		return nil, "", errors.New("no eligible uncertainty or plan exceeds 128 paths")
	}
	rels := make([]string, 0, len(selected))
	for _, e := range selected {
		if _, err := intentSafePath(s.wc, e); err != nil {
			return nil, "", err
		}
		rels = append(rels, e.Rel)
	}
	statuses, err := s.Cli.Status(ctx, s.wc, rels)
	if err != nil {
		return nil, "", err
	}
	status := make(map[string]string)
	for _, entry := range statuses {
		p := entry.Path
		if filepath.IsAbs(p) {
			p, err = filepath.Rel(s.wc, p)
			if err != nil {
				return nil, "", err
			}
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if entry.Props != "" && entry.Props != "none" && entry.Props != "normal" {
			return nil, "", errors.New("property changes prevent intent resolution")
		}
		if _, exists := status[p]; exists {
			return nil, "", errors.New("duplicate SVN status path")
		}
		status[p] = entry.Item
	}
	var paths []contract.IntentPath
	for _, e := range selected {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		abs, err := intentSafePath(s.wc, e)
		if err != nil {
			return nil, "", err
		}
		item := contract.IntentPath{Path: e.Rel, Operation: "delete"}
		if e.Op == "deleted" {
			if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) || status[e.Rel] != "missing" {
				return nil, "", fmt.Errorf("deletion no longer missing: %s", e.Rel)
			}
		} else {
			if status[e.Rel] != "unversioned" {
				return nil, "", fmt.Errorf("addition already scheduled or changed: %s", e.Rel)
			}
			item.Operation = "add"
			item.Size, item.SHA256, err = intentFileHash(ctx, abs)
			if err != nil {
				return nil, "", err
			}
		}
		paths = append(paths, item)
	}
	encoded, _ := json.Marshal(struct {
		Entries []cacheEntry
		Paths   []contract.IntentPath
	}{entries, paths})
	sum := sha256.Sum256(encoded)
	return paths, hex.EncodeToString(sum[:]), nil
}

func intentSafePath(wc string, e cacheEntry) (string, error) {
	if !safeIntentPath(e.Rel) {
		return "", errors.New("unsafe intent path")
	}
	parts := strings.Split(e.Rel, "/")
	for _, part := range parts {
		if part == "." || part == ".." || strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return "", errors.New("reserved intent path")
		}
	}
	abs := filepath.Join(wc, filepath.FromSlash(e.Rel))
	if filepath.Clean(e.Abs) != abs {
		return "", errors.New("intent path no longer belongs to working copy")
	}
	current := wc
	for i, part := range append([]string{""}, parts...) {
		if i > 0 {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && i == len(parts) && e.Op == "deleted" {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts) && !info.IsDir()) {
			return "", errors.New("indirect intent path is not supported")
		}
	}
	return abs, nil
}

// Called under wcOpMu and mu. A receipt is not an everlasting rule for a path:
// it only fences a rediscovery while the accepted Added item remains in queue.
func (s *Service) acceptedIntentReplay(it *stageItem, ev watcher.Event) bool {
	if it.Op != watcher.Added || it.ResolutionID == "" || it.MoveScheduled || it.OldRel != "" || ev.OldRel != "" || ev.IdentityVerified || ev.Type != watcher.EntryFile || ev.Path != it.Abs {
		return false
	}
	for _, r := range s.intentReceipts {
		if r.PlanID != it.ResolutionID || r.RepoID != s.repoID {
			continue
		}
		for _, p := range r.Paths {
			if p.Operation != "delete" {
				continue
			}
			abs := filepath.Join(s.wc, filepath.FromSlash(p.Path))
			if _, err := intentSafePath(s.wc, cacheEntry{Rel: p.Path, Abs: abs, Op: "deleted"}); err != nil {
				return false
			}
			if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		for _, p := range r.Paths {
			if p.Path != it.Rel || p.Operation != "add" {
				continue
			}
			abs, err := intentSafePath(s.wc, cacheEntry{Rel: it.Rel, Abs: it.Abs, Op: "added"})
			if err != nil {
				return false
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			size, hash, err := intentFileHash(ctx, abs)
			cancel()
			return err == nil && size == p.Size && hash == p.SHA256
		}
	}
	return false
}

func intentFileHash(ctx context.Context, path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !before.Mode().IsRegular() {
		return 0, "", errors.New("intent is not a regular file")
	}
	h := sha256.New()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	after, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return 0, "", errors.New("file changed while inspecting intent")
	}
	return before.Size(), hex.EncodeToString(h.Sum(nil)), nil
}
