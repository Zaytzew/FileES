package commit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// Implemented by the production client; test/legacy CLI clients without a
// native record may omit it. Native clients never silently fall back to CLI.
type commitWriterInspector interface {
	InspectCommitWriter(context.Context, string) (string, error)
}

// Before minting a new ID, retain the old transaction while its native fence
// still exists. This also exposes orphaned completed transactions in the GUI.
func (s *Service) requireIdleCommitWriter(ctx context.Context, wc string) error {
	inspector, ok := s.Cli.(commitWriterInspector)
	if !ok {
		return nil
	}
	id, err := inspector.InspectCommitWriter(ctx, wc)
	s.commitWriterBlocked = err != nil || id != ""
	if err != nil {
		return fmt.Errorf("inspect native commit record: %w", err)
	}
	if id != "" {
		return fmt.Errorf("native commit %s requires recovery before a new transaction", id)
	}
	return nil
}

// A legacy mismatched owner is NOT vouched for by the newer intent's baseline.
// Require its own retained transaction, identical WC/repository binding and a
// baseline the repository has still not reached. An absent log marker alone
// never suffices. The native release rechecks exact ownership under the OS lock.
func (s *Service) recoveryWriter(ctx context.Context, in *commitIntent, head int64) (string, string, error) {
	inspector, ok := s.Cli.(commitWriterInspector)
	if !ok {
		return "", "", nil
	}
	id, err := inspector.InspectCommitWriter(ctx, s.wc)
	if err != nil {
		return "", "", err
	}
	if id != "" {
		if _, ok := s.Cli.(commitWriterReleaser); !ok {
			return "", "", errors.New("native writer release unavailable")
		}
	}
	if id == "" || id == in.ID {
		return id, "", nil
	}
	previous, err := s.readIntentFile(s.wc, filepath.Join(s.wc, ".filees", "commit_cache", "previous-transaction.json"))
	if err != nil {
		return "", "", fmt.Errorf("read previous native owner proof: %w", err)
	}
	if previous == nil || previous.ID != id || previous.RepoURL != in.RepoURL || previous.RepoID != in.RepoID || previous.WC != in.WC || previous.Revision != 0 || head >= previous.FirstRevision || (previous.Phase != "attempting" && previous.Phase != "done") {
		return "", "", fmt.Errorf("native commit %s differs from intent %s; no matching no-effect proof, HOLD retained", id, in.ID)
	}
	return id, recoveryIntentDigest(previous), nil
}

func recoveryWriterID(plan *commitRecoveryPlanState, in *commitIntent) string {
	if plan.writerID != "" {
		return plan.writerID
	}
	return in.ID
}

// commitIntent contains only JSON-safe values; keep the complete proof bound
// to the plan, including WC/repository identity, phase, targets and audit.
func recoveryIntentDigest(in *commitIntent) string {
	raw, _ := json.Marshal(in)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
