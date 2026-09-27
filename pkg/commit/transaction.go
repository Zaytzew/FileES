package commit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/shout"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

type commitRecoveryPlanState struct {
	id, transactionID           string
	firstRevision, headRevision int64
	expires                     time.Time
	choice                      string
	conflicts                   []recoveryConflict
	copyDirectory               string
	writerID, writerProof       string
	transactionProof            string
}

// CommitRecoveryRequired is part of every repo.status the interface asks for.
// It must not wait for wcOpMu: a publication or update holds that lock while it
// talks to the server, and when the server's address stops answering the
// connect hangs for as long as the timeout. repo.status then outlived the
// interface's 10-second call limit and the interface reported the *daemon* as
// unreachable during a server outage (owner's report, 2026-09-24). While the
// lock is busy the last answer stands - a running publication is not one that
// needs recovery.
func (s *Service) CommitRecoveryRequired() bool {
	if !s.wcOpMu.TryLock() {
		return s.commitRecoveryCached.Load()
	}
	defer s.wcOpMu.Unlock()
	in, err := s.readIntent(s.wc)
	required := err != nil || (in != nil && in.Phase == "attempting") || s.commitWriterBlocked
	s.commitRecoveryCached.Store(required)
	return required
}

// PlanCommitRecovery proves that an attempted transaction has no possible
// receipt yet. It does not alter the transaction, watcher checkpoint or queue.
func (s *Service) PlanCommitRecovery(ctx context.Context) (*contract.CommitRecoveryPlan, error) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	in, err := s.readIntent(s.wc)
	if err != nil {
		return nil, fmt.Errorf("read attempted commit transaction: %w", err)
	}
	if in == nil || (in.Phase != "attempting" && !(in.Phase == "done" && in.Revision == 0)) {
		return nil, errors.New("no attempted commit transaction requires recovery")
	}
	if in.RepoID != s.repoID || in.RepoURL != s.RepoURL || in.WC != filepath.Clean(s.wc) {
		return nil, errors.New("recovery transaction belongs to another working copy or repository")
	}
	c, ok := s.Cli.(client.TransactionCommitter)
	if !ok {
		return nil, errors.New("commit recovery requires transaction-aware SVN client")
	}
	head, err := c.CommitHead(ctx, in.RepoURL)
	if err != nil {
		return nil, fmt.Errorf("read repository head: %w", err)
	}
	if head >= in.FirstRevision {
		return nil, errors.New("repository advanced; check the transaction receipt again")
	}
	now := time.Now()
	plan := &commitRecoveryPlanState{id: uuid.NewString(), transactionID: in.ID, firstRevision: in.FirstRevision, headRevision: head, expires: now.Add(2 * time.Minute)}
	plan.transactionProof = recoveryIntentDigest(in)
	plan.writerID, plan.writerProof, err = s.recoveryWriter(ctx, in, head)
	if err != nil {
		return nil, err
	}
	if in.Phase == "done" && plan.writerID == "" {
		return nil, errors.New("no pending native writer requires recovery")
	}
	plan.conflicts, err = s.inspectRecoveryConflicts(ctx, head)
	if err != nil {
		return nil, err
	}
	plan.choice = contract.CommitRecoveryRetryQueue
	var conflicts []string
	if len(plan.conflicts) > 0 {
		plan.choice = contract.CommitRecoveryServerCopy
		plan.copyDirectory = kolizjeDir + "/conflicted-copy-" + plan.id
		for _, conflict := range plan.conflicts {
			conflicts = append(conflicts, conflict.Path)
		}
	}
	s.commitRecoveryPlan = plan
	return &contract.CommitRecoveryPlan{PlanID: plan.id, RepoID: in.RepoID, TransactionID: in.ID, Choice: plan.choice, Conflicts: conflicts, ConflictCopy: plan.copyDirectory, FirstRevision: in.FirstRevision, HeadRevision: head, Paths: append([]string(nil), in.Paths...), ExpiresAt: plan.expires.UTC().Format(time.RFC3339Nano)}, nil
}

// ApplyCommitRecovery retires only an attempt proven to have had no remote
// effect. Staging and the publication observation are deliberately untouched.
func (s *Service) ApplyCommitRecovery(ctx context.Context, planID, choice string) (*contract.CommitRecoveryApplyResult, error) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	plan := s.commitRecoveryPlan
	s.commitRecoveryPlan = nil
	if plan == nil || plan.id != planID || choice != plan.choice || time.Now().After(plan.expires) {
		return nil, errors.New("commit recovery plan is absent, expired or mismatched")
	}
	in, err := s.readIntent(s.wc)
	if err != nil || in == nil || (in.Phase != "attempting" && !(in.Phase == "done" && in.Revision == 0)) || in.ID != plan.transactionID || in.FirstRevision != plan.firstRevision || recoveryIntentDigest(in) != plan.transactionProof {
		return nil, errors.New("commit transaction changed after the recovery plan")
	}
	c, ok := s.Cli.(client.TransactionCommitter)
	if !ok {
		return nil, errors.New("commit recovery requires transaction-aware SVN client")
	}
	head, err := c.CommitHead(ctx, in.RepoURL)
	if err != nil || head != plan.headRevision || head >= in.FirstRevision {
		return nil, errors.New("repository changed after the recovery plan")
	}
	writerID, proof, err := s.recoveryWriter(ctx, in, head)
	if err != nil || writerID != plan.writerID || proof != plan.writerProof {
		return nil, errors.New("native writer or its proof changed after recovery plan")
	}
	// The plan proved the attempt had no effect; the native helper still
	// holds its record and would refuse every later commit (E200004) until
	// released. Only this attempt's record is released.
	releaseWriter := func() error {
		if plan.writerID != "" {
			in.RecoveryWriterPlans = dedup(append(in.RecoveryWriterPlans, plan.writerID))
			if err := s.writeIntent(s.wc, in); err != nil {
				return err
			}
		}
		if releaser, ok := s.Cli.(commitWriterReleaser); ok {
			if err := releaser.ReleaseCommitWriter(ctx, s.wc, recoveryWriterID(plan, in)); err != nil {
				return fmt.Errorf("release the native commit record: %w", err)
			}
		}
		// A live helper can finish while copies are being prepared. Recheck
		// after its exclusive writer guard has proved it is no longer active.
		head, err := c.CommitHead(ctx, in.RepoURL)
		if err != nil || head != plan.headRevision {
			return errors.New("repository changed while preparing recovery")
		}
		return nil
	}
	if err := s.applyRecoveryConflicts(ctx, plan, in, releaseWriter); err != nil {
		return nil, err
	}
	if err := s.cleanRecoveryArtifacts(ctx, in); err != nil {
		return nil, err
	}
	in.Phase = "done"
	if err := s.writeIntent(s.wc, in); err != nil {
		return nil, err
	}
	if err := releaseIntentBusy(s.wc, in); err != nil {
		return nil, err
	}
	s.recoveryDiagnosticKey, s.recoveryDiagnosticAt = "", time.Time{}
	s.commitRecoveryCached.Store(false)
	s.commitWriterBlocked = false
	return &contract.CommitRecoveryApplyResult{PlanID: planID, State: "queued"}, nil
}

// commitWriterReleaser frees the native helper's record of one commit proven
// to have had no effect (client.execClient).
type commitWriterReleaser interface {
	ReleaseCommitWriter(ctx context.Context, wc, commitID string) error
}

const transactionSchema = "filees.commit-intent/v1"

// One per WC, serialized by wcOpMu. An attempted mutation is never reissued
// merely because its marker is currently absent: a remote transaction may
// still be completing. Confirmed effects are projected by idempotent upserts.
type commitIntent struct {
	Schema              string                       `json:"schema"`
	ID                  string                       `json:"id"`
	RepoURL             string                       `json:"repo_url"`
	RepoID              string                       `json:"repo_id"`
	WC                  string                       `json:"wc"`
	Phase               string                       `json:"phase"` // attempting, confirmed, empty, done
	FirstRevision       int64                        `json:"first_revision"`
	Revision            int64                        `json:"revision"`
	Comment             string                       `json:"comment,omitempty"`
	Paths               []string                     `json:"paths"`
	Items               []intentItem                 `json:"items"`
	Observation         *watcher.PublicationSnapshot `json:"observation,omitempty"`
	BusyMarker          string                       `json:"busy_marker,omitempty"`
	RecoveryWriterPlans []string                     `json:"recovery_writer_plans,omitempty"` // write-ahead audit of attempted native owner retirement
	ConflictCopies      []string                     `json:"conflict_copies,omitempty"`       // durable audit; never removed during retry
	ConflictArtifacts   []string                     `json:"conflict_artifacts,omitempty"`    // backed-up SVN artifacts accidentally scheduled as adds
}

type intentItem struct {
	Rel       string
	OldRel    string
	Op        watcher.OpType
	IsDir     bool
	FirstSeen time.Time
	LastSeen  time.Time
}

func transactionPath(wc string) string {
	return filepath.Join(wc, ".filees", "commit_cache", "transaction.json")
}

// HasUnresolvedCommit gates startup cleanup/update before Service.Run has
// loaded its identity/cache. Corrupt or unreadable state also holds the gate.
func HasUnresolvedCommit(wc string) bool {
	p := transactionPath(wc)
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() > 64*1024*1024 {
		return true
	}
	b, err := os.ReadFile(p)
	var in commitIntent
	return err != nil || json.Unmarshal(b, &in) != nil || in.Schema != transactionSchema || in.Phase != "done"
}

func safeIntentPath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return false
		}
	}
	return p != "" && p != "." && p != ".." && !filepath.IsAbs(p) && !strings.HasPrefix(p, "../") &&
		!strings.ContainsAny(p, "\\:\x00\r\n") && filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))) == p &&
		p != ".svn" && p != ".filees" && !strings.HasPrefix(p, ".svn/") && !strings.HasPrefix(p, ".filees/")
}

func (s *Service) readIntent(wc string) (*commitIntent, error) {
	return s.readIntentFile(wc, transactionPath(wc))
}

func (s *Service) readIntentFile(wc, p string) (*commitIntent, error) {
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64*1024*1024 {
		return nil, errors.New("invalid commit intent file")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var in commitIntent
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("commit intent: %w", err)
	}
	_, idErr := uuid.Parse(in.ID)
	if in.Schema != transactionSchema || idErr != nil || in.FirstRevision < 1 || len(in.Paths) == 0 || len(in.Paths) > 65536 {
		return nil, errors.New("commit intent identity mismatch")
	}
	if in.Phase != "attempting" && in.Phase != "confirmed" && in.Phase != "empty" && in.Phase != "done" {
		return nil, errors.New("invalid commit intent phase")
	}
	// A completed receipt carries no work to replay and must not prevent a
	// later legitimate move/rebind of the WC. Unfinished work stays bound.
	if in.Phase != "done" && (in.RepoURL != s.RepoURL || in.RepoID != s.repoID || in.WC != filepath.Clean(wc)) {
		return nil, errors.New("unfinished commit intent identity mismatch")
	}
	if in.Revision < 0 || (in.Phase == "confirmed" && in.Revision < in.FirstRevision) {
		return nil, errors.New("invalid commit receipt revision")
	}
	paths := make(map[string]bool)
	for _, p := range in.Paths {
		if !safeIntentPath(p) || paths[p] {
			return nil, errors.New("invalid commit intent targets")
		}
		paths[p] = true
	}
	for _, it := range in.Items {
		if !intentPathCovered(paths, it.Rel) || (it.OldRel != "" && !intentPathCovered(paths, it.OldRel)) || (it.Op != watcher.Added && it.Op != watcher.Modified && it.Op != watcher.Deleted && it.Op != watcher.Renamed) {
			return nil, errors.New("invalid commit intent item")
		}
	}
	if in.Observation != nil {
		if len(in.Observation.Entries) != len(paths) {
			return nil, errors.New("publication observation targets mismatch")
		}
		seen := make(map[string]bool)
		for _, e := range in.Observation.Entries {
			if !paths[e.Path] || seen[e.Path] {
				return nil, errors.New("publication observation targets mismatch")
			}
			seen[e.Path] = true
		}
	}
	return &in, nil
}

// A directory target commits its versioned descendants as one SVN transaction.
// Rename items therefore may name source files below a selected source directory
// even though the durable target list contains only that directory.
func intentPathCovered(targets map[string]bool, path string) bool {
	if !safeIntentPath(path) {
		return false
	}
	for candidate := path; candidate != "." && candidate != ""; candidate = filepath.ToSlash(filepath.Dir(filepath.FromSlash(candidate))) {
		if targets[candidate] {
			return true
		}
	}
	return false
}

func (s *Service) writeIntent(wc string, in *commitIntent) error {
	if !s.workingCopyAvailable(wc) {
		return errors.New("commit intent WC is unavailable")
	}
	old, err := s.readIntent(wc)
	if err != nil {
		return err
	}
	if old != nil && old.ID != in.ID {
		if old.Phase != "done" {
			return errors.New("cannot replace an unfinished commit intent")
		}
		previous := filepath.Join(wc, ".filees", "commit_cache", "previous-transaction.json")
		if s.RequireSVNMetadata {
			err = atomicWriteJSONSliceInExistingDir(previous, old)
		} else {
			err = atomicWriteJSONSlice(previous, old)
		}
		if err != nil {
			return err
		}
	}
	if s.RequireSVNMetadata {
		return atomicWriteJSONSliceInExistingDir(transactionPath(wc), in)
	}
	return atomicWriteJSONSlice(transactionPath(wc), in)
}

// intentPending is deliberately fail-closed, including corrupt/foreign state.
// It protects all clean-path retirement entry points, not only the poll loop.
func (s *Service) intentPending(wc string) bool {
	in, err := s.readIntent(wc)
	return err != nil || (in != nil && in.Phase != "done")
}

func (s *Service) recoverCommit(ctx context.Context, wc string) (found bool, resultErr error) {
	in, err := s.readIntent(wc)
	defer func() {
		resultErr = s.reportRecovery(in, resultErr, time.Now())
	}()
	if err != nil {
		return true, err
	}
	if in == nil || in.Phase == "done" {
		if err := s.requireIdleCommitWriter(ctx, wc); err != nil {
			return true, err
		}
		if in != nil {
			// Completed transactions are the durable source of truth for their
			// presentation too. Replaying this idempotent upsert repairs journals
			// written by older clients that skipped directory entries. A journal
			// failure must not turn a proven completed commit back into HOLD.
			if err := s.recordIntentPublishedActivity(in); err != nil {
				s.Logger.Warnf("repair completed commit activity: %v", err)
			}
			s.repairOrphanPendingActivity(ctx, wc)
			return false, releaseIntentBusy(wc, in)
		}
		return false, nil
	}
	done := s.setOperation("commit") // Existing daemon operation vocabulary; no renderer policy.
	defer done()
	if in.Phase == "attempting" {
		c, ok := s.Cli.(client.TransactionCommitter)
		if !ok {
			return true, errors.New("commit recovery requires transaction-aware SVN client")
		}
		rev, err := c.FindCommit(ctx, in.RepoURL, in.ID, in.FirstRevision)
		if err == nil && rev < in.FirstRevision {
			err = errors.New("no exact transaction receipt")
		}
		if err != nil {
			return true, fmt.Errorf("commit result remains uncertain; intent %s retained: %w", in.ID, err)
		}
		in.Phase, in.Revision = "confirmed", rev
		if err := s.writeIntent(wc, in); err != nil {
			return true, err
		}
	}
	if in.Phase == "confirmed" {
		if c, ok := s.Cli.(client.CommitReconciler); ok {
			if err := c.ReconcileCommit(ctx, wc, in.RepoURL, in.Paths, in.ID, in.Revision); err != nil {
				return true, fmt.Errorf("commit confirmed; local reconciliation pending: %w", err)
			}
		}
	}
	return true, s.finishIntent(ctx, wc, in)
}

func (s *Service) commitDurable(ctx context.Context, wc string, c client.TransactionCommitter, paths []string, message, comment string, pending []pendingEntry) error {
	old, err := s.readIntent(wc)
	if err != nil {
		return err
	}
	if old != nil && old.Phase != "done" {
		return errors.New("unfinished intent must be recovered before a new commit")
	}

	if err := s.requireIdleCommitWriter(ctx, wc); err != nil {
		return err
	}
	head, err := c.CommitHead(ctx, s.RepoURL)
	if err == nil && head < 0 {
		err = errors.New("invalid repository baseline")
	}
	if err != nil {
		return fmt.Errorf("commit baseline unavailable: %w", err)
	}
	in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: filepath.Clean(wc), Phase: "attempting", FirstRevision: head + 1, Paths: paths, Comment: comment}
	selected := make(map[string]bool)
	targetBytes := 0
	if len(paths) == 0 || len(paths) > 65536 {
		return errors.New("invalid durable commit target count")
	}
	for _, p := range paths {
		targetBytes += len(p) + 1
		if !safeIntentPath(p) || selected[p] || targetBytes > 16*1024*1024 {
			return errors.New("invalid durable commit target")
		}
		selected[p] = true
	}
	for _, pe := range pending {
		it := pe.item
		if selected[it.Rel] {
			in.Items = append(in.Items, intentItem{it.Rel, it.OldRel, it.Op, it.IsDir, it.FirstSeen, it.LastSeen})
		}
	}
	// Do not start a remote mutation if either the queued batch or its durable
	// transaction identity could be lost on process death.
	if s.CapturePublication != nil {
		var err error
		in.Observation, err = s.CapturePublication(paths)
		if err != nil {
			return err
		}
	}
	in.BusyMarker = fmt.Sprintf("transaction=%s\npid=%d\n", in.ID, os.Getpid())
	if err := s.writeStateString(filepath.Join(wc, ".filees", "state", "commit.busy"), in.BusyMarker); err != nil {
		return err
	}
	if err := s.saveCacheChecked(); err != nil {
		return err
	}
	if err := s.writeIntent(wc, in); err != nil {
		return err
	}
	report, endProgress := s.startPublishProgress(pending, selected)
	_, rev, commitErr := c.CommitWithID(client.WithCommitProgress(ctx, report), wc, s.RepoURL, paths, message, s.Rules.NeedsLock, in.ID, in.FirstRevision)
	endProgress()
	if commitErr != nil && errors.Is(commitErr, client.ErrCommitNotSent) {
		// Check the complete contract even with an older/misbehaving helper: a
		// not-sent response must not retire provenance while its fence survives.
		if err := s.requireIdleCommitWriter(ctx, wc); err != nil {
			return errors.Join(commitErr, err)
		}
		// Proven: the server never saw this transaction, and the helper has
		// released its writer record. Close the intent and keep the queue for
		// an ordinary retry; holding it for recovery was the loop the owner
		// kept answering with "Uzgodnij publikację" (2026-09-25).
		s.Logger.Warnf("commit refused locally before anything was sent: transaction=%s: %v", in.ID, commitErr)
		in.Phase = "done"
		if err := s.writeIntent(wc, in); err != nil {
			return errors.Join(commitErr, err)
		}
		return errors.Join(commitErr, releaseIntentBusy(wc, in))
	}
	if commitErr != nil {
		s.Logger.Warnf("commit reply failed: transaction=%s first_revision=%d; resolving receipt before any retry: %v", in.ID, in.FirstRevision, commitErr)
		// Same-context lookup may fail on cancellation. The next poll/startup
		// still has the identifier; never turn uncertainty into a new attempt.
		_, recoveryErr := s.recoverCommit(ctx, wc)
		if recoveryErr == nil {
			return nil
		}
		return errors.Join(commitErr, recoveryErr)
	}
	if rev == 0 {
		in.Phase = "empty"
	} else {
		if rev < in.FirstRevision {
			return errors.New("commit receipt precedes transaction baseline")
		}
		in.Phase, in.Revision = "confirmed", rev
	}
	if err := s.writeIntent(wc, in); err != nil {
		return err
	}
	return s.finishIntent(ctx, wc, in)
}

func (s *Service) finishIntent(ctx context.Context, wc string, in *commitIntent) error {
	if in.Phase == "empty" {
		in.Phase = "done"
		if err := s.writeIntent(wc, in); err != nil {
			return err
		}
		s.reconcileCleanPending(ctx, wc)
		return releaseIntentBusy(wc, in)
	}
	// Server success is already certain. A failed projection write leaves this
	// receipt in confirmed state for replay. Journal.Record upserts by repo/path.
	if in.Comment != "" {
		seen, exists, err := shout.LoadLastSeen(wc)
		if err != nil {
			return err
		}
		if !exists || seen < in.Revision {
			if err := shout.SaveLastSeen(wc, in.Revision); err != nil {
				return err
			}
		}
	}
	if err := s.recordIntentPublishedActivity(in); err != nil {
		return err
	}
	// A clean WC can retire this snapshot. Later edits remain staged. If C
	// died before updating WC metadata, don't reissue the remote transaction.
	entries, err := s.Cli.Status(ctx, wc, in.Paths)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Item == "added" || e.Item == "deleted" || e.Item == "replaced" || e.Item == "conflicted" {
			return errors.New("commit confirmed; local WC metadata still requires reconciliation")
		}
	}
	clean := s.cleanObservedPaths(ctx, wc, in.Paths)
	if s.AcknowledgePublication != nil {
		if err := s.AcknowledgePublication(in.Observation); err != nil {
			return err
		}
	}
	s.mu.Lock()
	for _, it := range in.Items {
		cur := s.staging[it.Rel]
		if cur == nil || cur.OldRel != it.OldRel || cur.Op != it.Op || !cur.FirstSeen.Equal(it.FirstSeen) || !cur.LastSeen.Equal(it.LastSeen) {
			continue
		}
		_, statErr := os.Lstat(filepath.Join(wc, filepath.FromSlash(it.Rel)))
		if clean[it.Rel] || (it.Op == watcher.Deleted && errors.Is(statErr, os.ErrNotExist)) {
			delete(s.staging, it.Rel)
		} else if it.Op == watcher.Renamed || it.Op == watcher.Added {
			// The original add/move was published; remaining content is a new
			// edit, never a second record-move or add of that same generation.
			cur.Op, cur.OldRel, cur.MoveScheduled, cur.RenameVerified = watcher.Modified, "", false, false
			cur.ver++
		} else if it.Op == watcher.Deleted && statErr == nil {
			cur.Op = watcher.Added // A new local object after the confirmed deletion.
			cur.ver++
		}
	}
	s.mu.Unlock()
	if err := s.saveCacheChecked(); err != nil {
		return err
	}
	head := filepath.Join(wc, ".filees", "state", "head.rev")
	if err := s.writeStateString(head, fmt.Sprintf("%d\n", in.Revision)); err != nil {
		return err
	}
	// Callback effects are idempotent state reconciliation. They can replay
	// after a crash; IPC notification is best effort, not an exactly-once bus.
	if s.OnPathsPublished != nil {
		var paths []string
		for _, it := range in.Items {
			if it.Op == watcher.Modified {
				paths = append(paths, filepath.Join(wc, filepath.FromSlash(it.Rel)))
			}
		}
		s.OnPathsPublished(paths)
	}
	if s.OnPathsRemoved != nil {
		var paths []string
		for _, it := range in.Items {
			if it.IsDir {
				continue
			}
			if it.Op == watcher.Deleted {
				paths = append(paths, filepath.Join(wc, filepath.FromSlash(it.Rel)))
			}
			if it.OldRel != "" {
				paths = append(paths, filepath.Join(wc, filepath.FromSlash(it.OldRel)))
			}
		}
		s.OnPathsRemoved(paths)
	}
	if s.OnBatchPublished != nil {
		s.OnBatchPublished()
	}
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		return err
	}
	if err := releaseIntentBusy(wc, in); err != nil {
		return err
	}
	// Optional presentation metadata must not turn a completed transaction into HOLD.
	s.recordLastCommit(ctx, wc, in.Revision)
	s.goOnline()
	s.lastCommit = time.Now()
	s.commitBatches.Add(1)
	s.mu.Lock()
	if in.Comment != "" && (s.shoutComment == "" || s.shoutComment == in.Comment) {
		s.shoutUsed, s.publishRevision = true, in.Revision
	}
	s.mu.Unlock()
	if s.OnHeadRevision != nil {
		s.OnHeadRevision(in.Revision)
	}
	if s.OnLastSync != nil {
		s.OnLastSync(time.Now())
	}
	s.emit(contract.EvCommitCompleted, contract.CommitCompletedPayload{Revision: in.Revision, Paths: len(in.Paths)})
	s.emit(contract.EvActivityChanged, nil)
	return nil
}

func (s *Service) recordIntentPublishedActivity(in *commitIntent) error {
	if s.Activity == nil || in == nil || in.Revision < 1 {
		return nil
	}
	for _, it := range in.Items {
		if err := s.Activity.Record(activity.Entry{RepoID: in.RepoID, Path: it.Rel, Kind: activity.Kind(opName(it.Op)), Stage: activity.Published, Revision: in.Revision}); err != nil {
			return err
		}
	}
	return nil
}

// Ownership is the durable transaction UUID, not the age or a reused PID.
// A completed old receipt cannot clear a newer operation's busy marker.
func releaseIntentBusy(wc string, in *commitIntent) error {
	if in.BusyMarker == "" {
		return nil
	}
	p := filepath.Join(wc, ".filees", "state", "commit.busy")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(b) != in.BusyMarker {
		return nil
	}
	return os.Remove(p)
}
