package servertool

import (
	"context"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"fmt"
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
	archiveRoot := r.DeletionArchiveRoot
	if archiveRoot == "" {
		archiveRoot = filepath.Join(r.ResultsRoot, "deleted-repositories")
	}
	publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: r.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}}
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
	worker := &repoworker.Worker{Backend: backend, Activator: effects, Capacity: capacity, Reservations: reservations, Store: store, MobilePairing: mobilePairingMinter{onboardingFiles}}
	dispatcher := repoworker.Dispatcher{Worker: worker, Resolver: repoworker.ViewResolver{ServiceWC: config.Activation.ServiceWorkingCopy}}
	if err := repoworker.WithFileLock(filepath.Join(r.ResultsRoot, ".worker.lock"), func() error {
		if _, err := repoworker.ReapDeletionArchives(archiveRoot, time.Now()); err != nil {
			return err
		}
		return dispatcher.Serve(context.Background(), args[0], in, out)
	}); err != nil {
		report(stderr, "repository worker", err)
		return ExitData
	}
	return ExitOK
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
