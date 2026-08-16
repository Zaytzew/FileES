package mobileworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"

	v1 "filees/pkg/mobile/v1"
)

// AppendReader is the read-only repository surface the append worker needs.
type AppendReader interface {
	Youngest(ctx context.Context, repoPath string) (int64, error)
	Stat(ctx context.Context, repoPath, p string, rev int64) (v1.Kind, bool, error)
	Cat(ctx context.Context, repoPath, p string, rev int64, w io.Writer) (int64, string, error)
}

// Committer publishes a new file. It is the authoritative uniqueness point: it
// must fail if the target already exists at commit time.
type Committer interface {
	// AppendFile commits spoolPath as the new file parent/filename with
	// svn:needs-lock and a filees:request-id revprop, returning the new revision.
	AppendFile(ctx context.Context, repoPath, parentPath, filename, spoolPath, requestID string) (int64, error)
}

// Appender serves UPLOAD_OBJECT: read-only for existing paths, append-only-unique
// for new ones. It never modifies or deletes an existing object.
type Appender struct {
	Authority Authority
	Reader    AppendReader
	Committer Committer
	Ledger    Ledger
	SpoolDir  string // "" -> os.TempDir()
}

// Upload spools and verifies the content, resolves a name collision content-aware
// (SAME -> drop, DIFF -> user decides), then publishes a unique append. It is
// idempotent on request_id via the ledger.
// requestID is the envelope request id (not a payload field); the append worker
// needs it for ledger idempotency and the commit revprop.
func (a Appender) Upload(ctx context.Context, clientID, requestID string, p v1.UploadObjectPayload, content io.Reader) (v1.UploadObjectResult, error) {
	view, err := a.Authority.Resolve(ctx, clientID, p.RepoID)
	if err != nil {
		return v1.UploadObjectResult{}, err
	}
	if view.Access != "rw" {
		return v1.UploadObjectResult{Outcome: v1.OutcomeAccessRevoked}, nil
	}
	target := path.Join(p.ParentPath, p.Filename)

	// Idempotency: a prior COMMITTED for this request_id returns its receipt and
	// never commits a second time.
	if rec, err := a.Ledger.Lookup(requestID); err != nil {
		return v1.UploadObjectResult{}, err
	} else if rec != nil && rec.State == v1.OpStateCommitted {
		if rec.PayloadHash != p.Sha256 {
			return v1.UploadObjectResult{}, errors.New("request_id reused with a different payload")
		}
		return v1.UploadObjectResult{Outcome: v1.OutcomeCommitted, Revision: rec.Revision, FinalPath: rec.FinalPath}, nil
	}

	spool, sum, size, err := a.spool(content)
	if err != nil {
		return v1.UploadObjectResult{}, err
	}
	defer os.Remove(spool)
	if sum != p.Sha256 {
		return v1.UploadObjectResult{}, errors.New("payload sha256 mismatch")
	}
	if p.Size != 0 && p.Size != size {
		return v1.UploadObjectResult{}, errors.New("payload size mismatch")
	}

	rev, err := a.Reader.Youngest(ctx, view.RepoPath)
	if err != nil {
		return v1.UploadObjectResult{}, err
	}

	if p.ParentPath != "" {
		kind, exists, err := a.Reader.Stat(ctx, view.RepoPath, p.ParentPath, rev)
		if err != nil {
			return v1.UploadObjectResult{}, err
		}
		// Missing parents are created in the same append commit. A file
		// sitting where the parent should be is DESTINATION_GONE.
		if exists && kind != v1.KindDirectory {
			return v1.UploadObjectResult{Outcome: v1.OutcomeDestGone}, nil
		}
	}

	if res, hit, err := a.collision(ctx, view.RepoPath, target, rev, sum); err != nil {
		return v1.UploadObjectResult{}, err
	} else if hit {
		return res, nil
	}

	base := Record{RequestID: requestID, ClientID: clientID, RepoID: p.RepoID, Path: target, PayloadHash: sum, State: v1.OpStateCommitting}
	if err := a.Ledger.Put(base); err != nil {
		return v1.UploadObjectResult{}, err
	}

	newRev, commitErr := a.Committer.AppendFile(ctx, view.RepoPath, p.ParentPath, p.Filename, spool, requestID)
	if commitErr != nil {
		// A racing client may have taken the name between the check and the
		// commit. Re-resolve against HEAD before surfacing the error.
		if head, herr := a.Reader.Youngest(ctx, view.RepoPath); herr == nil {
			if res, hit, cerr := a.collision(ctx, view.RepoPath, target, head, sum); cerr == nil && hit {
				a.reject(base)
				return res, nil
			}
		}
		a.reject(base)
		return v1.UploadObjectResult{}, commitErr
	}

	done := base
	done.State = v1.OpStateCommitted
	done.Revision = newRev
	done.FinalPath = target
	if err := a.Ledger.Put(done); err != nil {
		return v1.UploadObjectResult{}, err
	}
	return v1.UploadObjectResult{Outcome: v1.OutcomeCommitted, Revision: newRev, FinalPath: target}, nil
}

// collision resolves an existing target. A missing target is (hit=false). An
// existing file is hashed on demand and compared; an existing directory is a
// DIFF (a file can never equal a directory).
func (a Appender) collision(ctx context.Context, repoPath, target string, rev int64, uploadSum string) (v1.UploadObjectResult, bool, error) {
	kind, exists, err := a.Reader.Stat(ctx, repoPath, target, rev)
	if err != nil {
		return v1.UploadObjectResult{}, false, err
	}
	if !exists {
		return v1.UploadObjectResult{}, false, nil
	}
	if kind != v1.KindFile {
		return v1.UploadObjectResult{Outcome: v1.OutcomeNameTakenDiff}, true, nil
	}
	_, existingSum, err := a.Reader.Cat(ctx, repoPath, target, rev, io.Discard)
	if err != nil {
		return v1.UploadObjectResult{}, false, err
	}
	if existingSum == uploadSum {
		return v1.UploadObjectResult{Outcome: v1.OutcomeNameTakenSame, ExistingSha256: existingSum}, true, nil
	}
	return v1.UploadObjectResult{Outcome: v1.OutcomeNameTakenDiff, ExistingSha256: existingSum}, true, nil
}

func (a Appender) reject(rec Record) {
	rec.State = v1.OpStateRejected
	_ = a.Ledger.Put(rec)
}

// spool copies content to a private temp file while computing its SHA-256 and
// size. The caller removes the file.
func (a Appender) spool(content io.Reader) (spoolPath, sha string, size int64, err error) {
	f, err := os.CreateTemp(a.SpoolDir, "filees-mobile-spool-")
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), content)
	if err != nil {
		os.Remove(f.Name())
		return "", "", 0, err
	}
	if err := f.Sync(); err != nil {
		os.Remove(f.Name())
		return "", "", 0, err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), n, nil
}
