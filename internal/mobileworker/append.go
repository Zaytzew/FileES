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
	FileSize(ctx context.Context, repoPath, p string, rev int64) (int64, error)
	Cat(ctx context.Context, repoPath, p string, rev int64, w io.Writer) (int64, string, error)
}

// Committer publishes a new file. It is the authoritative uniqueness point: it
// must fail if the target already exists at commit time.
type Committer interface {
	// AppendFile commits spoolPath as the new file parent/filename with
	// svn:needs-lock and a filees:request-id revprop, returning the new revision.
	AppendFile(ctx context.Context, repoPath, parentPath, filename, spoolPath, requestID string) (int64, error)
	// CommitTree publishes many files under parentPath in one commit.
	// Replace marks an existing file that must be overwritten in HEAD.
	CommitTree(ctx context.Context, repoPath, parentPath string, files []TreeFile, requestID string) (int64, error)
}

// TreeFile is one extracted zip entry ready to land under parentPath.
type TreeFile struct {
	BaseRevision int64 // revision measured before recording the commit intent
	RelPath      string
	SpoolPath    string
	Replace      bool
	Sha          string
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
	// The phone's write boundary is authoritative here, not in its GUI.
	if !underMobileUploads(p.ParentPath) {
		return v1.UploadObjectResult{Outcome: v1.OutcomePolicyReject}, nil
	}
	target := path.Join(p.ParentPath, p.Filename)

	// Idempotency: a prior COMMITTED for this request_id returns its receipt and
	// never commits a second time.
	lock, err := a.Ledger.lockOperation(requestID)
	if err != nil {
		return v1.UploadObjectResult{}, err
	}
	defer lock.Close()
	ctx = context.WithValue(ctx, operationLockKey{}, lock)
	intent := Record{RepoID: p.RepoID, Path: target, PayloadHash: p.Sha256, Operation: v1.OpUploadObject, Size: p.Size}
	if rec, err := a.prior(ctx, clientID, requestID, view.RepoPath, intent); err != nil {
		return v1.UploadObjectResult{}, err
	} else if rec != nil && rec.State == v1.OpStateCommitted {
		if rec.Outcome == v1.OutcomeNameTakenSame {
			return v1.UploadObjectResult{Outcome: v1.OutcomeNameTakenSame, ExistingSha256: rec.PayloadHash}, nil
		}
		return v1.UploadObjectResult{Outcome: v1.OutcomeCommitted, Revision: rec.Revision, FinalPath: rec.FinalPath}, nil
	}

	if p.Size < 0 || p.Size > v1.MaxUploadBytes {
		return v1.UploadObjectResult{Outcome: v1.OutcomePolicyReject}, nil
	}
	spool, sum, size, err := a.spool(content, p.Size)
	if err != nil {
		return v1.UploadObjectResult{}, err
	}
	defer os.Remove(spool)
	if sum != p.Sha256 {
		return v1.UploadObjectResult{}, errors.New("payload sha256 mismatch")
	}
	if p.Size != size {
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
		// Missing directories inside the sandbox can be created; a file
		// at the requested parent is a domain failure.
		if exists && kind != v1.KindDirectory {
			return v1.UploadObjectResult{Outcome: v1.OutcomeDestGone}, nil
		}
	}

	if res, hit, err := a.collision(ctx, view.RepoPath, target, rev, sum); err != nil {
		return v1.UploadObjectResult{}, err
	} else if hit {
		if res.Outcome == v1.OutcomeNameTakenSame {
			done := intent
			done.RequestID, done.ClientID, done.State = requestID, clientID, v1.OpStateCommitted
			done.Revision, done.FinalPath, done.Outcome, done.NoChanges = rev, target, res.Outcome, true
			if err := a.Ledger.Put(done); err != nil {
				return v1.UploadObjectResult{}, err
			}
		}
		return res, nil
	}

	base := intent
	base.RequestID, base.ClientID, base.State, base.BeforeRevision = requestID, clientID, v1.OpStateCommitting, rev
	_, base.RecoveryFenced = a.Committer.(interface{ recoveryFence() })
	if err := a.Ledger.Put(base); err != nil {
		return v1.UploadObjectResult{}, err
	}

	newRev, commitErr := a.Committer.AppendFile(ctx, view.RepoPath, p.ParentPath, p.Filename, spool, requestID)
	if commitErr != nil {
		// A racing client may have taken the name between the check and the
		// commit. Re-resolve against HEAD before surfacing the error.
		if head, herr := a.Reader.Youngest(ctx, view.RepoPath); herr == nil {
			if res, hit, cerr := a.collision(ctx, view.RepoPath, target, head, sum); cerr == nil && hit {
				return res, nil
			}
		}
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

// spool copies content to a private temp file while computing its SHA-256 and
// size. The caller removes the file.
func (a Appender) spool(content io.Reader, limit int64) (spoolPath, sha string, size int64, err error) {
	f, err := os.CreateTemp(a.SpoolDir, "filees-mobile-spool-")
	if err != nil {
		return "", "", 0, err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	n, err := copyUploadBytes(io.MultiWriter(f, h), content, limit)
	if err != nil {
		return "", "", 0, err
	}
	if err := f.Sync(); err != nil {
		return "", "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", "", 0, err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), n, nil
}
