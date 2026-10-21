package repoworker

import (
	"context"
	"errors"
	"path/filepath"
)

// ArchiveFreezeService is a server-internal coordinator, with no control ticket
// or GUI action. Caller MUST hold the existing server writer exclusion,
// worker and service-WC locks. The supplied session barrier must be the real
// activation manager barrier, not a client acknowledgement or timer.
type ArchiveFreezeService struct {
	Authority           ServicePublisher
	Protection          ArchiveProtection
	Fence               ArchiveAdmissionFence
	WithSessionsStopped func(context.Context, func() error) error
}

// Child-process tests terminate at these durable boundaries.
var archiveFreezeCheckpoint = func(string) {}

func (s ArchiveFreezeService) Freeze(ctx context.Context, realm, repo, operation, repositoryUUID string, revision int64) (RepositoryArchivePolicy, error) {
	var result RepositoryArchivePolicy
	if !FileLocksSupported() {
		return result, errors.New("archive freeze requires Unix server file locks")
	}
	p := s.Authority
	if err := p.validateGrantPublisher(); err != nil {
		return result, err
	}
	record, err := p.loadActiveRepository(repo)
	if err != nil {
		return result, err
	}
	if record.OwnerRealmID != realm || record.Purpose != "" {
		return result, errors.New("authenticated realm does not own an ordinary archive candidate")
	}
	if filepath.Base(s.Protection.Repository) != repo {
		return result, errors.New("archive executor repository does not match authority")
	}
	want := RepositoryArchivePolicy{Schema: RepositoryArchivePolicySchema, OperationID: operation, RepositoryUUID: repositoryUUID, Revision: revision, Epoch: 1, State: "preparing", StartedAt: p.now()}
	if record.ArchivePolicy != nil {
		if record.ArchivePolicy.State == "released" {
			if record.ArchivePolicy.Epoch == ^uint64(0) || record.ArchivePolicy.OperationID == operation {
				return result, errors.New("refreeze requires a new policy operation and epoch")
			}
			want.Epoch = record.ArchivePolicy.Epoch + 1
		} else {
			want = *record.ArchivePolicy
			if want.State == "releasing" || want.OperationID != operation || want.RepositoryUUID != repositoryUUID || want.Revision != revision {
				return result, errors.New("archive operation conflicts with canonical intent")
			}
		}
	}
	if err := want.validate(); err != nil {
		return result, err
	}
	if s.WithSessionsStopped == nil {
		return result, errors.New("archive coordinator requires a session barrier")
	}
	if err := checkArchiveGuardExecutable(s.Protection.Executable); err != nil {
		return result, err
	}
	hooks, guardRoot, err := s.Protection.paths()
	if err != nil {
		return result, err
	}
	if err := inspectArchiveHooks(hooks, s.Protection.Executable, false); err != nil {
		return result, err
	}
	barrier := ArchiveProtection{WithSessionsStopped: s.WithSessionsStopped}
	err = barrier.withBarrier(ctx, func() error {
		// Commit winning before the drain is a stale plan, not an archive.
		head, _, err := s.Protection.snapshot(ctx)
		if err != nil {
			return err
		}
		if head.UUID != want.RepositoryUUID || head.Number != want.Revision {
			return errors.New("archive plan changed before the acknowledged drain")
		}
		if record.ArchivePolicy != nil && record.ArchivePolicy.State == "released" {
			if err := checkArchiveOperationReuse(guardRoot, operation, want.Epoch); err != nil {
				return err
			}
		}
		if err := s.Fence.Hold(ctx, repo, want); err != nil {
			return err
		}
		archiveFreezeCheckpoint("fenced")
		if record.ArchivePolicy == nil || record.ArchivePolicy.State == "released" {
			if err := p.publishArchivePolicy(ctx, record, want); err != nil {
				return err
			}
		}
		archiveFreezeCheckpoint("intent")
		local := s.Protection
		// We are already inside the real outer barrier. The local primitive
		// must not recursively acquire the activation manager lock.
		local.WithSessionsStopped = func(_ context.Context, fn func() error) error { return fn() }
		freezeErr := local.Freeze(ctx, ArchiveProtectionRecord{RepoID: repo, OperationID: operation, RepositoryUUID: repositoryUUID, Revision: revision, Epoch: want.Epoch})
		if freezeErr != nil && !errors.Is(freezeErr, ErrArchiveLocks) {
			return freezeErr
		}
		archiveFreezeCheckpoint("guarded")
		if err := inspectArchiveHooks(hooks, local.Executable, true); err != nil {
			return err
		}
		runtime, err := readArchiveProtection(local.Repository)
		if err != nil {
			return err
		}
		if runtime == nil || runtime.OperationID != operation || runtime.Epoch != want.Epoch || runtime.RepositoryUUID != repositoryUUID || runtime.Revision != revision {
			return errors.New("runtime archive protection does not match canonical intent")
		}
		if freezeErr == nil {
			want.State = "archived"
		} else {
			if want.State == "archived" {
				return errors.New("archived policy cannot regress to freezing")
			}
			want.State = "freezing"
		}
		if want.State == "archived" && runtime.State != "archived" {
			return errors.New("archive is not protected at the final boundary")
		}
		if err := p.publishArchivePolicy(ctx, record, want); err != nil {
			return err
		}
		archiveFreezeCheckpoint("published")
		// Never reopen raw writes until all guards AND canonical publication
		// succeeded. Freezing can then admit an ordinary unlock, not commits.
		if err := s.Fence.Open(ctx, repo, want); err != nil {
			return err
		}
		result = want
		archiveFreezeCheckpoint("opened")
		return freezeErr
	})
	return result, err
}
