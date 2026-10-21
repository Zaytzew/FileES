package repoworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// ArchiveReleaseRequest contains no actor/privilege claim. Administrator is a
// mandatory server adapter; ordinary authenticated repository ownership is NOT
// sufficient authority to remove archival protection.
type ArchiveReleaseRequest struct {
	RepoID, OperationID, ExpectedOperationID, RepositoryUUID string
	ExpectedEpoch                                            uint64
	Revision                                                 int64
	Reason                                                   string
}

func (r ArchiveReleaseRequest) validate() error {
	if r.ExpectedEpoch == 0 || r.ExpectedEpoch == ^uint64(0) || r.Revision < 0 || r.OperationID == r.ExpectedOperationID || !archiveReleaseText(r.Reason, 1024) {
		return errors.New("invalid archive release request")
	}
	for _, id := range []string{r.RepoID, r.OperationID, r.ExpectedOperationID, r.RepositoryUUID} {
		if p, err := uuid.Parse(id); err != nil || p.String() != id {
			return errors.New("archive release requires canonical identities")
		}
	}
	return nil
}

func (r ArchiveReleaseRequest) matches(old RepositoryArchivePolicy) bool {
	return old.State == "archived" && old.Epoch == r.ExpectedEpoch && old.OperationID == r.ExpectedOperationID && old.RepositoryUUID == r.RepositoryUUID && old.Revision == r.Revision
}

// ArchiveReleaseService requires the same writer/worker/service-WC exclusion
// as ArchiveFreezeService plus separate, server-owned administrator authority.
// Supplying an authenticated repository owner is not a valid admin adapter.
type ArchiveReleaseService struct {
	ArchiveFreezeService
	Administrator func(context.Context, string, RepositoryArchivePolicy) (string, error)
}

var archiveReleaseCheckpoint = func(string) {}

func (s ArchiveReleaseService) Release(ctx context.Context, req ArchiveReleaseRequest) (RepositoryArchivePolicy, error) {
	var result RepositoryArchivePolicy
	if !FileLocksSupported() {
		return result, errors.New("archive release requires Unix server file locks")
	}
	if err := req.validate(); err != nil {
		return result, err
	}
	if s.Administrator == nil || s.WithSessionsStopped == nil {
		return result, errors.New("administrative release requires separate authority and session barrier")
	}
	p := s.Authority
	if err := p.validateGrantPublisher(); err != nil {
		return result, err
	}
	record, err := p.loadActiveRepository(req.RepoID)
	if err != nil {
		return result, err
	}
	if record.Purpose != "" || record.ArchivePolicy == nil || filepath.Base(s.Protection.Repository) != req.RepoID {
		return result, errors.New("repository is not an ordinary protected archive")
	}
	current := *record.ArchivePolicy
	actor, err := s.Administrator(ctx, req.RepoID, current)
	if err != nil {
		return result, err
	}
	if !archiveReleaseText(actor, 128) {
		return result, errors.New("administrative release actor is absent")
	}
	if err := checkArchiveGuardExecutable(s.Protection.Executable); err != nil {
		return result, err
	}
	hooks, guardRoot, err := s.Protection.paths()
	if err != nil {
		return result, err
	}
	if err := inspectArchiveHooks(hooks, s.Protection.Executable, true); err != nil {
		return result, err
	}
	runtime, err := readArchiveProtection(s.Protection.Repository)
	if err != nil || runtime == nil {
		return result, errors.Join(err, errors.New("archive runtime is unavailable"))
	}
	want := current
	if current.State == "archived" {
		if !req.matches(current) || runtime.State != "archived" || runtime.OperationID != current.OperationID || runtime.Epoch != current.Epoch || runtime.RepositoryUUID != current.RepositoryUUID || runtime.Revision != current.Revision {
			return result, errors.New("archive release expected policy or runtime changed")
		}
		audit := ArchiveReleaseAudit{Schema: archiveReleaseAuditSchema, RepoID: req.RepoID, OperationID: req.OperationID, Actor: actor, Reason: req.Reason, RequestedAt: p.now(), Previous: current, PreviousSHA256: archivePolicyDigest(current), RuntimeBefore: *runtime}
		path, _ := canonicalArchiveReleasePath(p.ServiceWC, req.RepoID, current.Epoch+1)
		if prior, err := readCanonicalArchiveRelease(path); err == nil {
			audit.RequestedAt = prior.RequestedAt
			if prior != audit {
				return result, errors.New("archive release conflicts with an existing administrative audit")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		want = RepositoryArchivePolicy{Schema: RepositoryArchivePolicySchema, OperationID: req.OperationID, RepositoryUUID: current.RepositoryUUID, Revision: current.Revision, Epoch: current.Epoch + 1, State: "releasing", StartedAt: audit.RequestedAt, Release: &audit}
	} else {
		if (current.State != "releasing" && current.State != "released") || current.Release == nil || !req.matches(current.Release.Previous) || current.OperationID != req.OperationID || current.Release.Actor != actor || current.Release.Reason != req.Reason {
			return result, errors.New("archive release does not match the pending administrative operation")
		}
	}
	if err := want.validate(); err != nil {
		return result, err
	}
	if current.State == "released" {
		if !releasedRuntimeMatches(*runtime, want) {
			return result, errors.New("released runtime does not match canonical authority")
		}
		fence, err := s.Fence.read()
		if err != nil {
			return result, err
		}
		if fence == nil {
			return result, errors.New("archive release admission receipt is absent")
		}
		if fence.State == "open" || !fence.matches(req.RepoID, want) {
			return want, nil
		}
	}
	barrier := ArchiveProtection{WithSessionsStopped: s.WithSessionsStopped}
	err = barrier.withBarrier(ctx, func() error {
		confirmed, err := s.Administrator(ctx, req.RepoID, current)
		if err != nil {
			return err
		}
		if confirmed != actor {
			return errors.New("archive administrator changed before cutover")
		}
		if current.State != "released" {
			head, busy, err := s.Protection.snapshot(ctx)
			if err != nil {
				return err
			}
			if head.UUID != want.RepositoryUUID || head.Number != want.Revision || busy {
				return errors.New("archive release boundary is changed or locked")
			}
		}
		if err := checkArchiveOperationReuse(guardRoot, req.OperationID, want.Epoch); err != nil {
			return err
		}
		if err := s.Fence.Hold(ctx, req.RepoID, want); err != nil {
			return err
		}
		archiveReleaseCheckpoint("fenced")
		if current.State == "archived" {
			if err := p.publishArchivePolicy(ctx, record, want); err != nil {
				return err
			}
		}
		archiveReleaseCheckpoint("intent")
		local := s.Protection
		local.WithSessionsStopped = func(_ context.Context, fn func() error) error { return fn() }
		if err := local.Release(ctx, want.Release.RuntimeBefore, req.OperationID, req.Reason); err != nil {
			return err
		}
		archiveReleaseCheckpoint("runtime")
		got, err := readArchiveProtection(local.Repository)
		if err != nil || got == nil || !releasedRuntimeMatches(*got, want) {
			return errors.Join(err, errors.New("administrative runtime release was not verified"))
		}
		want.State = "released"
		if err := p.publishArchivePolicy(ctx, record, want); err != nil {
			return err
		}
		archiveReleaseCheckpoint("published")
		if err := s.Fence.Open(ctx, req.RepoID, want); err != nil {
			return err
		}
		result = want
		archiveReleaseCheckpoint("opened")
		return nil
	})
	return result, err
}

func releasedRuntimeMatches(r ArchiveProtectionRecord, p RepositoryArchivePolicy) bool {
	if p.Release == nil {
		return false
	}
	raw, _ := json.MarshalIndent(p.Release.RuntimeBefore, "", "  ")
	digest := sha256.Sum256(append(raw, '\n'))
	return r.State == "released" && r.RepoID == p.Release.RepoID && r.Epoch == p.Epoch && r.OperationID == p.OperationID && r.RepositoryUUID == p.RepositoryUUID && r.Revision == p.Revision && r.Reason == p.Release.Reason && r.PreviousSHA256 == hex.EncodeToString(digest[:])
}
