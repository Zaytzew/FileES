package mobileworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"filees/internal/storagewatch"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"os"
	"path/filepath"
)

type operationLockKey struct{}

var errOperationUncertain = errors.New("operation commit is not yet confirmed")

// lockOperation serializes the same request across worker processes. The
// lock inode is never unlinked: removing it could admit two distinct holders.
func (l Ledger) lockOperation(id string) (*os.File, error) {
	if err := os.MkdirAll(l.Dir, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(id))
	return storagewatch.Lock(filepath.Join(l.Dir, hex.EncodeToString(sum[:])+".lock"))
}

// recover only reads a prior intent. Absence of a revision is NOT evidence
// that an interrupted SVN subprocess cannot still commit; never replay an
// unresolved intent and overwrite a later desktop edit.
func (a Appender) recover(ctx context.Context, repoPath string, rec *Record) error {
	// Older workers marked ambiguous commit failures as rejected. That state
	// alone does not prove that SVN failed to publish. Recover their anchor
	// too; without a fence and an anchor they must remain uncertain.
	if rec.State == v1.OpStateRejected && !rec.RecoveryFenced {
		rec.State = v1.OpStateCommitting
	}
	if rec.State != v1.OpStateCommitting {
		return nil
	}
	reader, ok := a.Reader.(interface {
		FindRequestRevision(context.Context, string, string, int64) (int64, error)
	})
	if !ok {
		return errOperationUncertain
	}
	rev, err := reader.FindRequestRevision(ctx, repoPath, rec.RequestID, rec.BeforeRevision)
	if err != nil {
		return err
	}
	if rev == 0 {
		if rec.NoChanges {
			rev = rec.BeforeRevision
		} else if rec.RecoveryFenced {
			// The inherited process fence is held exclusively by us now.
			// No prior child can publish after this negative lookup.
			rec.State = v1.OpStateRejected
			return a.Ledger.Put(*rec)
		} else {
			return errOperationUncertain
		}
	}
	rec.State, rec.Revision, rec.FinalPath = v1.OpStateCommitted, rev, rec.Path
	return a.Ledger.Put(*rec)
}

func (a Appender) prior(ctx context.Context, clientID, requestID, repoPath string, intent Record) (*Record, error) {
	rec, err := a.Ledger.Lookup(requestID)
	if err != nil || rec == nil {
		return rec, err
	}
	if rec.ClientID != clientID || rec.RepoID != intent.RepoID || rec.Path != intent.Path || rec.PayloadHash != intent.PayloadHash ||
		(rec.Operation != "" && (rec.Operation != intent.Operation || rec.Size != intent.Size || rec.FileCount != intent.FileCount)) {
		return nil, errors.New("request_id reused with a different intent")
	}
	if err := a.recover(ctx, repoPath, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// FindRequestRevision scans bounded log pages after the durable pre-commit
// revision. It identifies OUR revision even if another writer advanced HEAD.
func (r SVNReader) FindRequestRevision(ctx context.Context, repoPath, requestID string, after int64) (int64, error) {
	head, err := r.Youngest(ctx, repoPath)
	if err != nil {
		return 0, err
	}
	for from := after + 1; from <= head; from += 256 {
		to := from + 255
		if to > head {
			to = head
		}
		raw, err := output(ctx, r.svn(), "log", "--xml", "--with-revprop", "filees:request-id", "-r", fmt.Sprintf("%d:%d", from, to), fileURL(repoPath))
		if err != nil {
			return 0, err
		}
		var log struct {
			Entries []struct {
				Revision int64 `xml:"revision,attr"`
				Props    []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:",chardata"`
				} `xml:"revprops>property"`
			} `xml:"logentry"`
		}
		if err := xml.Unmarshal(raw, &log); err != nil {
			return 0, err
		}
		for _, entry := range log.Entries {
			for _, prop := range entry.Props {
				if prop.Name == "filees:request-id" && prop.Value == requestID {
					return entry.Revision, nil
				}
			}
		}
	}
	return 0, nil
}
