package commit

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"

	"filees/pkg/errcat"
	"filees/pkg/errmap"
)

// Marks errors already journaled by recovery or intent HOLD so the publish caller does not log the
// same HOLD again. Wrapping preserves the native SVN/APR chain for inspection.
type recoveryFailure struct {
	cause  error
	detail string
}

func (e *recoveryFailure) Error() string { return e.detail }
func (e *recoveryFailure) Unwrap() error { return e.cause }

// Called under wcOpMu, including startup, poll and explicit publication.
// No state transition, retry or filesystem cleanup depends on this diagnostic.
func (s *Service) reportRecovery(in *commitIntent, err error, now time.Time) error {
	if err == nil {
		s.confirmRecoveryDiagnostic(in, now)
		if s.recoveryDiagnosticKey != "" {
			s.Logger.Infof("commit recovery resumed; previous HOLD cleared")
		}
		s.recoveryDiagnosticKey, s.recoveryDiagnosticAt = "", time.Time{}
		return nil
	}
	s.recoveryJournalChecked = ""
	id, phase := "unavailable", "unreadable"
	var revision int64
	var targets int
	if in != nil {
		id, phase, revision, targets = in.ID, in.Phase, in.Revision, len(in.Paths)
	}
	// No argv, URLs, comments, credentials or target lists are added here.
	// The cause retains existing bounded helper diagnostics and numeric codes.
	detail := fmt.Sprintf("commit recovery HOLD: transaction=%s phase=%s revision=%d targets=%d; no commit retry; cause=%v", id, phase, revision, targets, err)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(detail)))
	if len(detail) > 8192 {
		detail = detail[:8192] + " [diagnostic truncated]"
	}
	fault := errcat.New(errcat.KeyCommitRecoveryHeld, map[string]string{"detail": detail}, err)
	if key != s.recoveryDiagnosticKey || now.Sub(s.recoveryDiagnosticAt) >= 15*time.Minute {
		entry := errmap.Classify(fault)
		entry.Details = detail
		s.ErrSink.Emit(entry)
		s.Logger.Warnf("%s", detail)
		s.recoveryDiagnosticKey, s.recoveryDiagnosticAt = key, now
	}
	return &recoveryFailure{cause: fault, detail: detail}
}

// Called only after recoverCommit has really succeeded. Replay after restart
// also closes legacy HOLD records for this exact retained transaction. A failed
// journal read remains retryable and does not manufacture a confirmation.
func (s *Service) confirmRecoveryDiagnostic(in *commitIntent, now time.Time) {
	if in == nil || s.recoveryJournalChecked == in.ID || s.wc == "" {
		return
	}
	pending, err := errmap.RecoveryPending(filepath.Join(s.wc, ".filees", "logs", "errors.jsonl"), in.ID)
	if err != nil {
		s.Logger.Warnf("inspect recovery diagnostic history: %v", err)
		return
	}
	if !pending {
		s.recoveryJournalChecked = in.ID
		return
	}
	entry := errmap.Classify(errcat.New(errcat.KeyCommitRecoveryResolved, nil, nil))
	entry.Details = fmt.Sprintf("commit recovery confirmed: transaction=%s revision=%d", in.ID, in.Revision)
	s.ErrSink.EmitAt(now, entry)
	// Re-read on the next successful inspection. Sink is best-effort: a failed
	// append must not permanently suppress the durable closing record.
}
