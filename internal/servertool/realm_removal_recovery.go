package servertool

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
)

// recoverPendingRealmRemovals is invoked only by an explicit server recovery
// action. It provides the communication-independent continuation required
// after RevokeRealm has made every client credential unusable.
func recoverPendingRealmRemovals(ctx context.Context, config serverconfig.Config) (int, error) {
	r := config.Repositories
	removalRoot := filepath.Join(r.ResultsRoot, "realm-removals")
	if _, err := os.Stat(removalRoot); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	archiveRoot := r.DeletionArchiveRoot
	if archiveRoot == "" {
		archiveRoot = filepath.Join(r.ResultsRoot, "deleted-repositories")
	}
	runner := repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}
	publisher := repoworker.ServicePublisher{
		ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: r.DataAuthzFile, Runner: runner,
	}
	effects := repoworker.ServerEffects{
		SVNAdmin: r.SVNAdminBinary, RepositoriesRoot: r.Root, DataAuthzFile: r.DataAuthzFile,
		DeletionArchiveRoot: archiveRoot, DeletionRetentionDays: r.EffectiveDeletionRetentionDays(),
		Authority: publisher,
	}
	backend := &repoworker.DurableBackend{
		Root: filepath.Join(r.ResultsRoot, "backend"), URLPrefix: r.URLPrefix, Effects: effects,
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		return 0, err
	}
	store := repoworker.RealmRemovalStore{
		Root: removalRoot, OTPPepper: config.Onboarding.OTPPepper,
		TTL: config.Onboarding.OperationTTL, Attempts: config.Onboarding.OTPAttempts,
	}
	recovery := realmRecoveryPublisher{
		ArchiveRoot: archiveRoot,
		Manifests:   repoworker.RecoveryManifestStore{Root: filepath.Join(r.ResultsRoot, "recovery-manifests")},
		Keys:        repoworker.RecoveryKeyStore{Root: filepath.Join(r.ResultsRoot, "recovery-keys")},
	}
	var publicShareChannels *channel.Store
	if config.PublicShares.Enabled {
		publicShareChannels = &channel.Store{Root: config.PublicShares.EffectiveStateRoot(r.ResultsRoot)}
	}
	executor := realmRemovalExecutor{
		Store: store, Backend: backend, Recovery: recovery, Publisher: publisher, Activation: manager,
		Erasure:        repoworker.DataErasureStore{Root: filepath.Join(r.ResultsRoot, "data-erasure")},
		PublicShares:   publicShareChannels,
		ErasureMaxDays: r.EffectiveDataErasureMaxDays(),
	}
	pending, err := store.PendingConfirmed()
	if err != nil {
		return 0, err
	}
	for _, record := range pending {
		if err := executor.Execute(ctx, record); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}
