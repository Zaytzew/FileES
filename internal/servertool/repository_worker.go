package servertool

import (
	"context"
	"fmt"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"io"
	"path/filepath"
	"time"
)

func RunRepositoryWorker(args []string, in io.Reader, out, stderr io.Writer) int {
	return runRepositoryWorker("/etc/filees/server.json", args, in, out, stderr)
}
func runRepositoryWorker(configPath string, args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "filees-worker repository-control: client ID required")
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation|serverconfig.SecretOTP)
	if err != nil {
		report(stderr, "repository worker config", err)
		return ExitConfig
	}
	r := config.Repositories
	if !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.ResultsRoot) || !filepath.IsAbs(r.DataAuthzFile) || !filepath.IsAbs(r.SVNAdminBinary) || r.URLPrefix == "" {
		fmt.Fprintln(stderr, "repository worker: repository configuration is incomplete")
		return ExitConfig
	}
	if err := repoworker.ValidateURLPrefix(r.URLPrefix); err != nil {
		report(stderr, "repository worker url_prefix", err)
		return ExitConfig
	}
	archiveRoot := r.DeletionArchiveRoot
	if archiveRoot == "" {
		archiveRoot = filepath.Join(r.ResultsRoot, "deleted-repositories")
	}
	runner := repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}
	publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: r.DataAuthzFile, Runner: runner}
	effects := repoworker.ServerEffects{SVNAdmin: r.SVNAdminBinary, RepositoriesRoot: r.Root, DataAuthzFile: r.DataAuthzFile, DeletionArchiveRoot: archiveRoot, DeletionRetentionDays: r.EffectiveDeletionRetentionDays(), Authority: publisher}
	backend := &repoworker.DurableBackend{Root: filepath.Join(r.ResultsRoot, "backend"), URLPrefix: r.URLPrefix, Effects: effects}
	store, err := repoworker.NewFileStore(filepath.Join(r.ResultsRoot, "results"))
	if err != nil {
		report(stderr, "repository worker store", err)
		return ExitConfig
	}
	capacity := repoworker.FilesystemCapacity{Root: r.Root}
	reservations := &repoworker.FileReservationLedger{Root: filepath.Join(r.ResultsRoot, "reservations"), Capacity: capacity}
	onboardingFiles, err := onboarding.OpenPrepared(config.Root, config.Onboarding, onboarding.Access{Areas: onboarding.AreaOperations | onboarding.AreaAudit, NeedOTP: true})
	if err != nil {
		report(stderr, "repository worker onboarding", err)
		return ExitConfig
	}
	aliases := repoworker.RealmAliases{ServiceWC: config.Activation.ServiceWorkingCopy, Runner: runner}
	activationManager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "repository worker activation", err)
		return ExitConfig
	}
	realmRemovalStore := repoworker.RealmRemovalStore{Root: filepath.Join(r.ResultsRoot, "realm-removals"), OTPPepper: config.Onboarding.OTPPepper, TTL: config.Onboarding.OperationTTL, Attempts: config.Onboarding.OTPAttempts}
	recoveryPublisher := realmRecoveryPublisher{
		ArchiveRoot: archiveRoot,
		Manifests:   repoworker.RecoveryManifestStore{Root: filepath.Join(r.ResultsRoot, "recovery-manifests")},
		Keys:        repoworker.RecoveryKeyStore{Root: filepath.Join(r.ResultsRoot, "recovery-keys")},
	}
	realmRemoval := realmRemovalCoordinator{
		Store:         realmRemovalStore,
		SnapshotScope: publisher.SnapshotRealmScope,
		ActiveClients: activationManager.ActiveClientsInRealm,
		Execute:       realmRemovalExecutor{Store: realmRemovalStore, Backend: backend, Recovery: recoveryPublisher, Publisher: publisher, Activation: activationManager}.Execute,
		Manifests:     recoveryPublisher.Manifests,
	}
	worker := &repoworker.Worker{Backend: backend, Activator: effects, Capacity: capacity, Reservations: reservations, Store: store, MobilePairing: mobilePairingMinter{onboardingFiles}, Aliases: aliases, ClientDetacher: clientDetacher{manager: activationManager}, RealmRemoval: realmRemoval}
	dispatcher := repoworker.Dispatcher{Worker: worker, Resolver: repoworker.ViewResolver{ServiceWC: config.Activation.ServiceWorkingCopy}}
	if err := repoworker.WithFileLock(filepath.Join(r.ResultsRoot, ".worker.lock"), func() error {
		if err := backend.ReapFailedCreates(context.Background()); err != nil {
			return err
		}
		if _, err := repoworker.ReapDeletionArchives(archiveRoot, time.Now()); err != nil {
			return err
		}
		if _, err := reapRecoveryCapabilities(r.ResultsRoot, time.Now()); err != nil {
			return err
		}
		return dispatcher.Serve(context.Background(), args[0], in, out)
	}); err != nil {
		report(stderr, "repository worker", err)
		return ExitData
	}
	return ExitOK
}

type clientDetacher struct{ manager *activation.Manager }

func (d clientDetacher) DetachClient(ctx context.Context, clientID string) (int64, error) {
	if d.manager == nil {
		return 0, fmt.Errorf("activation manager is unavailable")
	}
	return d.manager.Revoke(ctx, clientID, "client requested server detach")
}

// mobilePairingMinter adapts onboarding.Files.CreateMobilePairing to
// repoworker.MobilePairingMinter.
type mobilePairingMinter struct{ files *onboarding.Files }

func (m mobilePairingMinter) CreatePairing(realmID string, repos []repoworker.MobilePairingRepoGrant) (string, time.Time, error) {
	grants := make([]onboarding.MobileRepositoryGrant, 0, len(repos))
	for _, r := range repos {
		grants = append(grants, onboarding.MobileRepositoryGrant{RepoID: r.RepoID, Access: r.Access, AttachmentPolicy: r.AttachmentPolicy})
	}
	token, receipt, err := m.files.CreateMobilePairing(realmID, grants)
	return token, receipt.ExpiresAt, err
}
