package mobileworker

import (
	"context"
	"encoding/json"
	"errors"
	"filees/internal/fsdurable"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reconcile is an offline operator action, never exposed to the phone. Retry
// permission requires the operator to have stopped ALL old worker/SVN processes
// and disabled new mobile sessions. Old children did not inherit our lock.
// Run as the ledger owner to preserve access to atomically replaced records.
func (a Appender) Reconcile(ctx context.Context, id string, allowRetry bool, reason string) (Record, error) {
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return Record{}, errors.New("canonical request UUID required")
	}
	if allowRetry && (strings.TrimSpace(reason) == "" || len(reason) > 512 || strings.ContainsAny(reason, "\r\n")) {
		return Record{}, errors.New("a single-line operator reason is required (max 512 bytes)")
	}
	lock, err := a.Ledger.lockOperation(id)
	if err != nil {
		return Record{}, fmt.Errorf("operation busy or lock unavailable: %w", err)
	}
	defer lock.Close()
	rec, err := a.Ledger.Lookup(id)
	if err != nil {
		return Record{}, err
	}
	if rec == nil {
		return Record{}, errors.New("operation not found")
	}
	before := *rec
	if rec.RequestID != id {
		return before, errors.New("record identity mismatch")
	}
	if rec.State == v1.OpStateCommitted {
		return before, nil
	}
	if rec.State != v1.OpStateCommitting && rec.State != v1.OpStateRejected {
		return before, errors.New("operation state cannot be reconciled")
	}
	view, err := a.Authority.Resolve(ctx, rec.ClientID, rec.RepoID)
	if err != nil {
		return before, err
	}
	reader, ok := a.Reader.(interface {
		FindRequestRevision(context.Context, string, string, int64) (int64, error)
	})
	if !ok {
		return before, errors.New("recovery reader unavailable")
	}
	// Administrative recovery always scans the full history, even for legacy
	// records with absent or inaccurate pre-commit metadata.
	rev, err := reader.FindRequestRevision(ctx, view.RepoPath, id, 0)
	if err != nil {
		return before, err
	}
	if rev > 0 {
		rec.State, rec.Revision, rec.FinalPath = v1.OpStateCommitted, rev, rec.Path
	} else if rec.NoChanges && rec.BeforeRevision > 0 {
		rec.State, rec.Revision, rec.FinalPath = v1.OpStateCommitted, rec.BeforeRevision, rec.Path
	} else {
		if !allowRetry {
			return before, errOperationUncertain
		}
		rec.State, rec.RecoveryFenced, rec.Revision, rec.FinalPath = v1.OpStateRejected, true, 0, ""
	}
	// Persist the before image and intended decision before changing the ledger.
	// A crash can leave a planned audit with no mutation; repeat is safe.
	audit := struct {
		Time            string
		UID             int
		Reason          string
		Before, Planned Record
	}{time.Now().UTC().Format(time.RFC3339Nano), os.Geteuid(), reason, before, *rec}
	raw, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return before, err
	}
	f, err := os.CreateTemp(a.Ledger.Dir, "recovery-"+id+"-*.json")
	if err != nil {
		return before, err
	}
	name := f.Name()
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return before, err
	}
	if err = fsdurable.SyncDir(filepath.Dir(name)); err != nil {
		return before, err
	}
	if err = a.Ledger.Put(*rec); err != nil {
		return before, err
	}
	return *rec, nil
}
